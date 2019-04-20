package tasks

import (
	"context"

	"yunion.io/x/jsonutils"

	api "yunion.io/x/onecloud/pkg/apis/compute"
	"yunion.io/x/onecloud/pkg/cloudcommon/db"
	"yunion.io/x/onecloud/pkg/cloudcommon/db/taskman"
	"yunion.io/x/onecloud/pkg/compute/models"
	"yunion.io/x/onecloud/pkg/util/logclient"
)

type CloudProviderDeleteTask struct {
	taskman.STask
}

func init() {
	taskman.RegisterTask(CloudProviderDeleteTask{})
}

func (self *CloudProviderDeleteTask) OnInit(ctx context.Context, obj db.IStandaloneModel, body jsonutils.JSONObject) {
	provider := obj.(*models.SCloudprovider)

	provider.SetStatus(self.UserCred, api.CLOUD_PROVIDER_DELETING, "StartDiskCloudproviderTask")

	err := provider.RealDelete(ctx, self.UserCred)
	if err != nil {
		provider.SetStatus(self.UserCred, api.CLOUD_PROVIDER_DELETE_FAILED, "StartDiskCloudproviderTask")
		self.SetStageFailed(ctx, err.Error())
		logclient.AddActionLogWithStartable(self, provider, logclient.ACT_DELETE, err.Error(), self.UserCred, false)
		return
	}

	provider.SetStatus(self.UserCred, api.CLOUD_PROVIDER_DELETED, "StartDiskCloudproviderTask")

	self.SetStageComplete(ctx, nil)
	logclient.AddActionLogWithStartable(self, provider, logclient.ACT_DELETE, nil, self.UserCred, true)
}
