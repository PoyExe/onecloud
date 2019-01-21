package huawei

import (
	"context"
	"fmt"
	"strings"

	"yunion.io/x/jsonutils"
	"yunion.io/x/log"
	"yunion.io/x/onecloud/pkg/cloudprovider"
	"yunion.io/x/onecloud/pkg/compute/models"
)

type ImageOwnerType string

const (
	ImageOwnerPublic ImageOwnerType = "gold"    // 公共镜像：gold
	ImageOwnerSelf   ImageOwnerType = "private" // 私有镜像：private
	ImageOwnerShared ImageOwnerType = "shared"  // 共享镜像：shared
)

const (
	ImageStatusQueued  = "queued"  // queued：表示镜像元数据已经创建成功，等待上传镜像文件。
	ImageStatusSaving  = "saving"  // saving：表示镜像正在上传文件到后端存储。
	ImageStatusDeleted = "deleted" // deleted：表示镜像已经删除。
	ImageStatusKilled  = "killed"  // killed：表示镜像上传错误。
	ImageStatusActive  = "active"  // active：表示镜像可以正常使用
)

// https://support.huaweicloud.com/api-ims/zh-cn_topic_0020091565.html
type SImage struct {
	storageCache *SStoragecache

	Schema             string `json:"schema"`
	MinDisk            int64  `json:"min_disk"`
	CreatedAt          string `json:"created_at"`
	ImageSourceType    string `json:"__image_source_type"`
	ContainerFormat    string `json:"container_format"`
	File               string `json:"file"`
	UpdatedAt          string `json:"updated_at"`
	Protected          bool   `json:"protected"`
	Checksum           string `json:"checksum"`
	SupportKVMFPGAType string `json:"__support_kvm_fpga_type"`
	ID                 string `json:"id"`
	Isregistered       string `json:"__isregistered"`
	MinRAM             int64  `json:"min_ram"`
	Lazyloading        string `json:"__lazyloading"`
	Owner              string `json:"owner"`
	OSType             string `json:"__os_type"`
	Imagetype          string `json:"__imagetype"`
	Visibility         string `json:"visibility"`
	VirtualEnvType     string `json:"virtual_env_type"`
	Platform           string `json:"__platform"`
	SizeGB             int    `json:"size"`
	OSBit              string `json:"__os_bit"`
	OSVersion          string `json:"__os_version"`
	Name               string `json:"name"`
	Self               string `json:"self"`
	DiskFormat         string `json:"disk_format"`
	Status             string `json:"status"`
}

func (self *SImage) GetId() string {
	return self.ID
}

func (self *SImage) GetName() string {
	return self.Name
}

func (self *SImage) GetGlobalId() string {
	return self.ID
}

func (self *SImage) GetStatus() string {
	switch self.Status {
	case ImageStatusQueued:
		return models.IMAGE_STATUS_QUEUED
	case ImageStatusActive:
		return models.IMAGE_STATUS_ACTIVE
	case ImageStatusKilled:
		return models.IMAGE_STATUS_KILLED
	default:
		return models.IMAGE_STATUS_KILLED
	}
}

func (self *SImage) Refresh() error {
	new, err := self.storageCache.region.GetImage(self.GetId())
	if err != nil {
		return err
	}
	return jsonutils.Update(self, new)
}

func (self *SImage) IsEmulated() bool {
	return false
}

func (self *SImage) GetMetadata() *jsonutils.JSONDict {
	data := jsonutils.NewDict()
	if len(self.OSBit) > 0 {
		data.Add(jsonutils.NewString(self.OSBit), "os_arch")
	}
	if len(self.OSType) > 0 {
		data.Add(jsonutils.NewString(self.OSType), "os_name")
	}
	if len(self.Platform) > 0 {
		data.Add(jsonutils.NewString(self.Platform), "os_distribution")
	}
	if len(self.OSVersion) > 0 {
		data.Add(jsonutils.NewString(self.OSVersion), "os_version")
	}
	return data
}

func (self *SImage) Delete(ctx context.Context) error {
	return self.storageCache.region.DeleteImage(self.GetId())
}

func (self *SImage) GetIStoragecache() cloudprovider.ICloudStoragecache {
	return self.storageCache
}

func (self *SRegion) GetImage(imageId string) (SImage, error) {
	image := SImage{}
	err := DoGet(self.ecsClient.Images.Get, imageId, nil, &image)
	return image, err
}

func (self *SRegion) GetImages(status string, imagetype ImageOwnerType, name string, limit int, marker string) ([]SImage, int, error) {
	querys := map[string]string{}
	if len(status) > 0 {
		querys["status"] = status
	}

	if len(imagetype) > 0 {
		querys["__imagetype"] = string(imagetype)
	}

	if len(name) > 0 {
		querys["name"] = name
	}

	if len(marker) > 0 {
		querys["marker"] = marker
	}

	images := make([]SImage, 0)
	err := DoList(self.ecsClient.Images.List, querys, &images)
	return images, len(images), err
}

func (self *SRegion) DeleteImage(imageId string) error {
	return DoDelete(self.ecsClient.OpenStackImages.Delete, imageId, nil, nil)
}

func (self *SRegion) GetImageByName(name string) (*SImage, error) {
	if len(name) == 0 {
		return nil, fmt.Errorf("image name should not be empty")
	}

	images, _, err := self.GetImages("", ImageOwnerType(""), name, 1, "")
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, cloudprovider.ErrNotFound
	}

	log.Debugf("%d image found match name %s", len(images), name)
	return &images[0], nil
}

/* https://support.huaweicloud.com/api-ims/zh-cn_topic_0020092109.html
   os version 取值范围： https://support.huaweicloud.com/api-ims/zh-cn_topic_0031617666.html
   用于创建私有镜像的源云服务器系统盘大小大于等于40GB且不超过1024GB。
   目前支持vhd，zvhd、raw，qcow2
   todo: 考虑使用镜像快速导入。 https://support.huaweicloud.com/api-ims/zh-cn_topic_0133188204.html
   使用OBS文件创建镜像

   * openstack原生接口支持的格式：https://support.huaweicloud.com/api-ims/zh-cn_topic_0031615566.html
*/
func (self *SRegion) ImportImageJob(name string, osDist string, osVersion string, osArch string, bucket string, key string, minDiskGB int64) (string, error) {
	os_version, err := stdVersion(osDist, osVersion, osArch)
	log.Debugf("%s %s %s: %s", osDist, osVersion, osArch, os_version)
	if err != nil {
		log.Debugf(err.Error())
	}

	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(name), "name")
	image_url := fmt.Sprintf("%s:%s", bucket, key)
	params.Add(jsonutils.NewString(image_url), "image_url")
	if len(os_version) > 0 {
		params.Add(jsonutils.NewString(os_version), "os_version")
	}
	params.Add(jsonutils.NewBool(true), "is_config_init")
	params.Add(jsonutils.NewBool(true), "is_config")
	params.Add(jsonutils.NewInt(minDiskGB), "min_disk")

	ret, err := self.ecsClient.Images.PerformAction2("action", "", params, "")
	if err != nil {
		return "", err
	}

	return ret.GetString("job_id")
}

func formatVersion(osDist string, osVersion string) (string, error) {
	err := fmt.Errorf("unsupport version %s.reference: https://support.huaweicloud.com/api-ims/zh-cn_topic_0031617666.html", osVersion)
	dist := strings.ToLower(osDist)
	if dist == "ubuntu" || dist == "redhat" || dist == "centos" || dist == "oracle" || dist == "euleros" {
		parts := strings.Split(osVersion, ".")
		if len(parts) < 2 {
			return "", err
		}

		return parts[0] + "." + parts[1], nil
	}

	if dist == "debian" {
		parts := strings.Split(osVersion, ".")
		if len(parts) < 3 {
			return "", err
		}

		return parts[0] + "." + parts[1] + "." + parts[2], nil
	}

	if dist == "fedora" || dist == "windows" || dist == "suse" {
		parts := strings.Split(osVersion, ".")
		if len(parts) < 1 {
			return "", err
		}

		return parts[0], nil
	}

	if dist == "opensuse" {
		parts := strings.Split(osVersion, ".")
		if len(parts) == 0 {
			return "", err
		}

		if len(parts) == 1 {
			return parts[0], nil
		}

		if len(parts) >= 2 {
			return parts[0] + "." + parts[1], nil
		}
	}

	return "", err
}

// todo: 如何保持同步更新
// https://support.huaweicloud.com/api-ims/zh-cn_topic_0031617666.html
func stdVersion(osDist string, osVersion string, osArch string) (string, error) {
	// 架构
	arch := ""
	switch osArch {
	case "64", "x86_64":
		arch = "64bit"
	case "32", "x86_32":
		arch = "32bit"
	default:
		return "", fmt.Errorf("unsupported arch %s.reference: https://support.huaweicloud.com/api-ims/zh-cn_topic_0031617666.html", osArch)
	}

	_dist := strings.Split(strings.TrimSpace(osDist), " ")[0]
	_dist = strings.ToLower(_dist)
	// 版本
	ver, err := formatVersion(_dist, osVersion)
	if err != nil {
		return "", err
	}

	//  操作系统
	dist := ""

	switch _dist {
	case "ubuntu":
		return fmt.Sprintf("Ubuntu %s server %s", ver, arch), nil
	case "redhat":
		dist = "Redhat Linux Enterprise"
	case "centos":
		dist = "CentOS"
	case "fedora":
		dist = "Fedora"
	case "debian":
		dist = "Debian GNU/Linux"
	case "windows":
		dist = "Windows Server"
	case "oracle":
		dist = "Oracle Linux Server release"
	case "suse":
		dist = "SUSE Linux Enterprise Server"
	case "opensuse":
		dist = "OpenSUSE"
	case "euleros":
		dist = "EulerOS"
	default:
		return "", fmt.Errorf("unsupported os %s. reference: https://support.huaweicloud.com/api-ims/zh-cn_topic_0031617666.html", dist)
	}

	return fmt.Sprintf("%s %s %s", dist, ver, arch), nil
}
