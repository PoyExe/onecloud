// Copyright 2019 Yunion
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package models

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"reflect"
	"text/template"
	"time"

	"yunion.io/x/jsonutils"
	"yunion.io/x/log"
	"yunion.io/x/pkg/errors"
	"yunion.io/x/pkg/gotypes"
	"yunion.io/x/sqlchemy"

	"yunion.io/x/onecloud/pkg/apis"
	api "yunion.io/x/onecloud/pkg/apis/compute"
	identity_apis "yunion.io/x/onecloud/pkg/apis/identity"
	"yunion.io/x/onecloud/pkg/cloudcommon/db"
	"yunion.io/x/onecloud/pkg/cloudcommon/validators"
	"yunion.io/x/onecloud/pkg/compute/options"
	"yunion.io/x/onecloud/pkg/httperrors"
	"yunion.io/x/onecloud/pkg/mcclient"
	"yunion.io/x/onecloud/pkg/mcclient/auth"
	"yunion.io/x/onecloud/pkg/util/stringutils2"
)

type SLoadbalancerAgentManager struct {
	SLoadbalancerLogSkipper
	db.SStandaloneResourceBaseManager
	SLoadbalancerClusterResourceBaseManager
}

var LoadbalancerAgentManager *SLoadbalancerAgentManager

func init() {
	gotypes.RegisterSerializable(reflect.TypeOf(&SLoadbalancerAgentParams{}), func() gotypes.ISerializable {
		return &SLoadbalancerAgentParams{}
	})
	LoadbalancerAgentManager = &SLoadbalancerAgentManager{
		SStandaloneResourceBaseManager: db.NewStandaloneResourceBaseManager(
			SLoadbalancerAgent{},
			"loadbalanceragents_tbl",
			"loadbalanceragent",
			"loadbalanceragents",
		),
	}
	LoadbalancerAgentManager.SetVirtualObject(LoadbalancerAgentManager)
}

// TODO
//
//  - scrub stale backends: Guests with deleted=1
//  - agent configuration params
//
type SLoadbalancerAgent struct {
	db.SStandaloneResourceBase
	SLoadbalancerClusterResourceBase `width:"36" charset:"ascii" nullable:"false" list:"user" create:"required"`

	Version    string                    `width:"64" nullable:"true" list:"admin" update:"admin"`
	IP         string                    `width:"32" nullable:"true" list:"admin" update:"admin"`
	HaState    string                    `width:"32" nullable:"true" list:"admin" update:"admin" default:"UNKNOWN"` // LB_HA_STATE_UNKNOWN
	HbLastSeen time.Time                 `nullable:"true" list:"admin" update:"admin"`
	HbTimeout  int                       `nullable:"true" list:"admin" update:"admin" create:"optional" default:"3600"`
	Params     *SLoadbalancerAgentParams `create:"optional" list:"admin" get:"admin"`

	Loadbalancers             time.Time `nullable:"true" list:"admin" update:"admin"`
	LoadbalancerListeners     time.Time `nullable:"true" list:"admin" update:"admin"`
	LoadbalancerListenerRules time.Time `nullable:"true" list:"admin" update:"admin"`
	LoadbalancerBackendGroups time.Time `nullable:"true" list:"admin" update:"admin"`
	LoadbalancerBackends      time.Time `nullable:"true" list:"admin" update:"admin"`
	LoadbalancerAcls          time.Time `nullable:"true" list:"admin" update:"admin"`
	LoadbalancerCertificates  time.Time `nullable:"true" list:"admin" update:"admin"`

	Deployment *SLoadbalancerAgentDeployment `create:"optional" list:"admin" get:"admin"`
	// ClusterId  string                        `width:"36" charset:"ascii" nullable:"false" list:"user" create:"required"`
}

type SLoadbalancerAgentParamsVrrp struct {
	Priority          int
	VirtualRouterId   int
	GarpMasterRefresh int
	Preempt           bool
	Interface         string
	AdvertInt         int
	Pass              string
}

type SLoadbalancerAgentParamsHaproxy struct {
	GlobalLog      string
	GlobalNbthread int
	LogHttp        bool
	LogTcp         bool
	LogNormal      bool
	TuneHttpMaxhdr int
}

type SLoadbalancerAgentParamsTelegraf struct {
	InfluxDbOutputUrl       string
	InfluxDbOutputName      string
	InfluxDbOutputUnsafeSsl bool
	HaproxyInputInterval    int
}

type SLoadbalancerAgentParams struct {
	KeepalivedConfTmpl string
	HaproxyConfTmpl    string
	TelegrafConfTmpl   string
	Vrrp               SLoadbalancerAgentParamsVrrp
	Haproxy            SLoadbalancerAgentParamsHaproxy
	Telegraf           SLoadbalancerAgentParamsTelegraf
}

func (p *SLoadbalancerAgentParamsVrrp) Validate(data *jsonutils.JSONDict) error {
	if len(p.Interface) == 0 || len(p.Interface) > 16 {
		// TODO printable exclude white space
		return httperrors.NewInputParameterError("invalid vrrp interface %q", p.Interface)
	}
	if len(p.Pass) == 0 || len(p.Pass) > 8 {
		// TODO printable exclude white space
		return httperrors.NewInputParameterError("invalid vrrp authentication pass size: %d, want [1,8]", len(p.Pass))
	}
	if p.Priority < 1 || p.Priority > 255 {
		return httperrors.NewInputParameterError("invalid vrrp priority %d: want [1,255]", p.Priority)
	}
	if p.VirtualRouterId < 1 || p.VirtualRouterId > 255 {
		return httperrors.NewInputParameterError("invalid vrrp virtual_router_id %d: want [1,255]", p.VirtualRouterId)
	}
	return nil
}

func (p *SLoadbalancerAgentParamsVrrp) validatePeer(pp *SLoadbalancerAgentParamsVrrp) error {
	if p.Priority == pp.Priority {
		return fmt.Errorf("vrrp priority of peer lbagents must be different, got %d", p.Priority)
	}
	if p.VirtualRouterId != pp.VirtualRouterId {
		return fmt.Errorf("vrrp virtual_router_id of peer lbagents must be the same: %d != %d", p.VirtualRouterId, pp.VirtualRouterId)
	}
	if p.AdvertInt != pp.AdvertInt {
		return fmt.Errorf("vrrp advert_int of peer lbagents must be the same: %d != %d", p.AdvertInt, pp.AdvertInt)
	}
	if p.Preempt != pp.Preempt {
		return fmt.Errorf("vrrp preempt property of peer lbagents must be the same: %v != %v", p.Preempt, pp.Preempt)
	}
	if p.Pass != pp.Pass {
		return fmt.Errorf("vrrp password of peer lbagents must be the same: %q != %q", p.Pass, pp.Pass)
	}
	return nil
}

func (p *SLoadbalancerAgentParamsVrrp) needsUpdatePeer(pp *SLoadbalancerAgentParamsVrrp) bool {
	// properties no need to check: Priority
	if p.VirtualRouterId != pp.VirtualRouterId {
		return true
	}
	if p.AdvertInt != pp.AdvertInt {
		return true
	}
	if p.Preempt != pp.Preempt {
		return true
	}
	if p.Pass != pp.Pass {
		return true
	}
	return false
}

func (p *SLoadbalancerAgentParamsVrrp) updateBy(pp *SLoadbalancerAgentParamsVrrp) {
	p.VirtualRouterId = pp.VirtualRouterId
	p.AdvertInt = pp.AdvertInt
	p.Preempt = pp.Preempt
	p.Pass = pp.Pass
}

func (p *SLoadbalancerAgentParamsVrrp) initDefault(data *jsonutils.JSONDict) {
	if !data.Contains("params", "vrrp", "advert_int") {
		p.AdvertInt = 1
	}
	if !data.Contains("params", "vrrp", "garp_master_refresh") {
		p.GarpMasterRefresh = 27
	}
	if !data.Contains("params", "vrrp", "pass") {
		p.Pass = "YunionLB"
	}
}

func (p *SLoadbalancerAgentParamsHaproxy) Validate(data *jsonutils.JSONDict) error {
	if p.GlobalNbthread < 1 {
		p.GlobalNbthread = 1
	}
	if p.GlobalNbthread > 64 {
		// This is a limit imposed by haproxy and arch word size
		p.GlobalNbthread = 64
	}
	if p.TuneHttpMaxhdr < 0 {
		p.TuneHttpMaxhdr = 0
	}
	if p.TuneHttpMaxhdr > 32767 {
		p.TuneHttpMaxhdr = 32767
	}
	return nil
}

func (p *SLoadbalancerAgentParamsHaproxy) initDefault(data *jsonutils.JSONDict) {
	if !data.Contains("params", "haproxy", "global_nbthread") {
		p.GlobalNbthread = 1
	}
	if !data.Contains("params", "haproxy", "global_log") {
		p.GlobalLog = "log /dev/log local0 info"
	}
	if !data.Contains("params", "haproxy", "log_http") {
		p.LogHttp = true
	}
	if !data.Contains("params", "haproxy", "log_normal") {
		p.LogNormal = true
	}
}

func (p *SLoadbalancerAgentParamsTelegraf) Validate(data *jsonutils.JSONDict) error {
	if p.InfluxDbOutputUrl != "" {
		_, err := url.Parse(p.InfluxDbOutputUrl)
		if err != nil {
			return httperrors.NewInputParameterError("telegraf params: invalid influxdb url: %s", err)
		}
	}
	if p.HaproxyInputInterval <= 0 {
		p.HaproxyInputInterval = 5
	}
	if p.InfluxDbOutputName == "" {
		p.InfluxDbOutputName = "telegraf"
	}
	return nil
}

func (p *SLoadbalancerAgentParamsTelegraf) initDefault(data *jsonutils.JSONDict) {
	if p.InfluxDbOutputUrl == "" {
		baseOpts := &options.Options
		u, _ := auth.GetServiceURL("influxdb", baseOpts.Region, "",
			identity_apis.EndpointInterfacePublic)
		p.InfluxDbOutputUrl = u
	}
	if p.HaproxyInputInterval == 0 {
		p.HaproxyInputInterval = 5
	}
	if p.InfluxDbOutputName == "" {
		p.InfluxDbOutputName = "telegraf"
	}
}

func (p *SLoadbalancerAgentParams) validateTmpl(k, s string) error {
	d, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return httperrors.NewInputParameterError("%s: bad base64 encoded string: %s", k, err)
	}
	s = string(d)
	_, err = template.New("").Parse(s)
	if err != nil {
		return httperrors.NewInputParameterError("%s: bad template: %s", k, err)
	}
	return nil
}

func (p *SLoadbalancerAgentParams) initDefault(data *jsonutils.JSONDict) {
	if p.KeepalivedConfTmpl == "" {
		p.KeepalivedConfTmpl = loadbalancerKeepalivedConfTmplDefaultEncoded
	}
	if p.HaproxyConfTmpl == "" {
		p.HaproxyConfTmpl = loadbalancerHaproxyConfTmplDefaultEncoded
	}
	if p.TelegrafConfTmpl == "" {
		p.TelegrafConfTmpl = loadbalancerTelegrafConfTmplDefaultEncoded
	}
	p.Vrrp.initDefault(data)
	p.Haproxy.initDefault(data)
	p.Telegraf.initDefault(data)
}

func (p *SLoadbalancerAgentParams) Validate(data *jsonutils.JSONDict) error {
	p.initDefault(data)
	if err := p.validateTmpl("keepalived_conf_tmpl", p.KeepalivedConfTmpl); err != nil {
		return err
	}
	if err := p.validateTmpl("haproxy_conf_tmpl", p.HaproxyConfTmpl); err != nil {
		return err
	}
	if err := p.validateTmpl("telegraf_conf_tmpl", p.TelegrafConfTmpl); err != nil {
		return err
	}
	if err := p.Vrrp.Validate(data); err != nil {
		return err
	}
	if err := p.Haproxy.Validate(data); err != nil {
		return err
	}
	if err := p.Telegraf.Validate(data); err != nil {
		return err
	}
	return nil
}

func (p *SLoadbalancerAgentParams) String() string {
	return jsonutils.Marshal(p).String()
}

func (p *SLoadbalancerAgentParams) IsZero() bool {
	if *p == (SLoadbalancerAgentParams{}) {
		return true
	}
	return false
}

func (man *SLoadbalancerAgentManager) ValidateCreateData(ctx context.Context, userCred mcclient.TokenCredential, ownerId mcclient.IIdentityProvider, query jsonutils.JSONObject, data *jsonutils.JSONDict) (*jsonutils.JSONDict, error) {
	clusterV := validators.NewModelIdOrNameValidator("cluster", "loadbalancercluster", ownerId)
	paramsV := validators.NewStructValidator("params", &SLoadbalancerAgentParams{})
	{
		keyV := map[string]validators.IValidator{
			"hb_timeout": validators.NewNonNegativeValidator("hb_timeout").Default(3600),
			"params":     paramsV,
			"cluster":    clusterV,
		}
		for _, v := range keyV {
			if err := v.Validate(data); err != nil {
				return nil, err
			}
		}
	}
	{
		cluster := clusterV.Model.(*SLoadbalancerCluster)
		lbagents, err := LoadbalancerClusterManager.getLoadbalancerAgents(cluster.Id)
		if err != nil {
			return nil, httperrors.NewGeneralError(err)
		}
		params := paramsV.Value.(*SLoadbalancerAgentParams)
		vrrpRouterId := params.Vrrp.VirtualRouterId
		for i := range lbagents {
			peerLbagent := &lbagents[i]
			peerParams := peerLbagent.Params
			err := params.Vrrp.validatePeer(&peerParams.Vrrp)
			if err != nil {
				return nil, httperrors.NewConflictError("conflict with lbagent %s(%s): %v", peerLbagent.Name, peerLbagent.Id, err)
			}
		}
		otherCluster, err := LoadbalancerClusterManager.findByVrrpRouterIdInZone(cluster.ZoneId, vrrpRouterId)
		if err != nil {
			return nil, err
		}
		if otherCluster != nil && otherCluster.Id != cluster.Id {
			return nil, httperrors.NewConflictError("lbcluster %s(%s) already has virtual_router_id %d",
				otherCluster.Name, otherCluster.Id, vrrpRouterId)
		}
	}

	input := apis.StandaloneResourceCreateInput{}
	err := data.Unmarshal(&input)
	if err != nil {
		return nil, httperrors.NewInternalServerError("unmarshal StandaloneResourceCreateInput fail %s", err)
	}
	input, err = man.SStandaloneResourceBaseManager.ValidateCreateData(ctx, userCred, ownerId, query, input)
	if err != nil {
		return nil, err
	}
	data.Update(jsonutils.Marshal(input))
	return data, nil
}

// 负载均衡Agent列表
func (man *SLoadbalancerAgentManager) ListItemFilter(
	ctx context.Context,
	q *sqlchemy.SQuery,
	userCred mcclient.TokenCredential,
	query api.LoadbalancerAgentListInput,
) (*sqlchemy.SQuery, error) {
	q, err := man.SStandaloneResourceBaseManager.ListItemFilter(ctx, q, userCred, query.StandaloneResourceListInput)
	if err != nil {
		return nil, errors.Wrap(err, "SStandaloneResourceBaseManager.ListItemFilter")
	}
	q, err = man.SLoadbalancerClusterResourceBaseManager.ListItemFilter(ctx, q, userCred, query.LoadbalancerClusterFilterListInput)
	if err != nil {
		return nil, errors.Wrap(err, "SLoadbalancerClusterResourceBaseManager.ListItemFilter")
	}

	if len(query.Version) > 0 {
		q = q.In("version", query.Version)
	}
	if len(query.IP) > 0 {
		q = q.In("ip", query.IP)
	}
	if len(query.HaState) > 0 {
		q = q.In("ha_state", query.HaState)
	}

	return q, nil
}

func (man *SLoadbalancerAgentManager) OrderByExtraFields(
	ctx context.Context,
	q *sqlchemy.SQuery,
	userCred mcclient.TokenCredential,
	query api.LoadbalancerAgentListInput,
) (*sqlchemy.SQuery, error) {
	var err error

	q, err = man.SStandaloneResourceBaseManager.OrderByExtraFields(ctx, q, userCred, query.StandaloneResourceListInput)
	if err != nil {
		return nil, errors.Wrap(err, "SStandaloneResourceBaseManager.OrderByExtraFields")
	}
	q, err = man.SLoadbalancerClusterResourceBaseManager.ListItemFilter(ctx, q, userCred, query.LoadbalancerClusterFilterListInput)
	if err != nil {
		return nil, errors.Wrap(err, "SLoadbalancerClusterResourceBaseManager.ListItemFilter")
	}

	return q, nil
}

func (man *SLoadbalancerAgentManager) QueryDistinctExtraField(q *sqlchemy.SQuery, field string) (*sqlchemy.SQuery, error) {
	var err error

	q, err = man.SStandaloneResourceBaseManager.QueryDistinctExtraField(q, field)
	if err == nil {
		return q, nil
	}
	q, err = man.SLoadbalancerClusterResourceBaseManager.QueryDistinctExtraField(q, field)
	if err == nil {
		return q, nil
	}

	return q, httperrors.ErrNotFound
}

func (man *SLoadbalancerAgentManager) CleanPendingDeleteLoadbalancers(ctx context.Context, userCred mcclient.TokenCredential, isStart bool) {
	agents := []SLoadbalancerAgent{}
	{
		// find active agents
		err := man.Query().All(&agents)
		if err != nil {
			log.Errorf("query agents failed")
			return
		}
		i := 0
		for _, agent := range agents {
			if !agent.IsActive() {
				continue
			}
			agents[i] = agent
			i++
		}
		agents = agents[:i]
	}
	men := map[string]db.IModelManager{
		"loadbalancers":               LoadbalancerManager,
		"loadbalancer_listeners":      LoadbalancerListenerManager,
		"loadbalancer_listener_rules": LoadbalancerListenerRuleManager,
		"loadbalancer_backend_groups": LoadbalancerBackendGroupManager,
		"loadbalancer_backends":       LoadbalancerBackendManager,
		"loadbalancer_acls":           LoadbalancerAclManager,
		"loadbalancer_certificates":   LoadbalancerCertificateManager,
	}
	agentsData := jsonutils.Marshal(&agents).(*jsonutils.JSONArray)
	for fieldName, man := range men {
		keyPlural := man.KeywordPlural()
		now := time.Now()
		minT := now
		if len(agents) > 0 {
			// find min updated_at seen by these active agents
			for i := 0; i < agentsData.Length(); i++ {
				agentData, _ := agentsData.GetAt(i)
				t, err := agentData.GetTime(fieldName)
				if err != nil {
					continue
				}
				if minT.After(t) {
					minT = t
				}
			}
			if minT.Equal(now) {
				log.Warningf("%s: no agents has reported yet", keyPlural)
				continue
			}
		} else {
			// when no active agents exists, we are free to go
		}
		{
			// find resources pending deleted before minT
			q := man.Query().IsTrue("pending_deleted").LT("pending_deleted_at", minT)
			rows, err := q.Rows()
			if err != nil {
				log.Errorf("%s: query pending_deleted_at < %s: %s", keyPlural, minT, err)
				continue
			}
			defer rows.Close()
			m, err := db.NewModelObject(man)
			if err != nil {
				log.Errorf("%s: new model object failed: %s", keyPlural, err)
				continue
			}
			mInitValue := reflect.Indirect(reflect.ValueOf(m))
			m, _ = db.NewModelObject(man)
			for rows.Next() {
				reflect.Indirect(reflect.ValueOf(m)).Set(mInitValue)
				err := q.Row2Struct(rows, m)
				if err != nil {
					log.Errorf("%s: Row2Struct: %s", keyPlural, err)
					continue
				}
				{
					// find real delete method
					rv := reflect.Indirect(reflect.ValueOf(m))
					baseRv := rv.FieldByName("SVirtualResourceBase")
					if !baseRv.IsValid() {
						baseRv = rv.FieldByName("SSharableVirtualResourceBase")
					}
					if !baseRv.IsValid() {
						log.Errorf("%s: cannot find base resource field", keyPlural)
						break // no need to try again
					}
					// now update deleted,deleted_at fields
					realDeleteMethod := baseRv.Addr().MethodByName("Delete")
					retRv := realDeleteMethod.Call([]reflect.Value{
						reflect.ValueOf(ctx),
						reflect.ValueOf(userCred),
					})
					err := retRv[0].Interface()
					if !gotypes.IsNil(err) {
						log.Errorf("%s: real delete failed: %s", keyPlural, err.(error))
					}
				}
			}
		}
	}
}

func (lbagent *SLoadbalancerAgent) ValidateUpdateData(ctx context.Context, userCred mcclient.TokenCredential, query jsonutils.JSONObject, data *jsonutils.JSONDict) (*jsonutils.JSONDict, error) {
	{
		keyV := map[string]validators.IValidator{
			"hb_timeout": validators.NewNonNegativeValidator("hb_timeout").Optional(true),
		}
		for _, v := range keyV {
			if err := v.Validate(data); err != nil {
				return nil, err
			}
		}
	}
	keys := map[string]time.Time{
		"loadbalancers":               lbagent.Loadbalancers,
		"loadbalancer_listeners":      lbagent.LoadbalancerListeners,
		"loadbalancer_listener_rules": lbagent.LoadbalancerListenerRules,
		"loadbalancer_backend_groups": lbagent.LoadbalancerBackendGroups,
		"loadbalancer_backends":       lbagent.LoadbalancerBackends,
		"loadbalancer_acls":           lbagent.LoadbalancerAcls,
		"loadbalancer_certificates":   lbagent.LoadbalancerCertificates,
	}
	for k, curValue := range keys {
		if !data.Contains(k) {
			continue
		}
		newValue, err := data.GetTime(k)
		if err != nil {
			return nil, httperrors.NewInputParameterError("%s: time error: %s", k, err)
		}
		if newValue.Before(curValue) {
			// this is possible with objects deleted
			data.Remove(k)
			continue
		}
		if now := time.Now(); newValue.After(now) {
			return nil, httperrors.NewInputParameterError("%s: new time is in the future: %s > %s",
				k, newValue, now)
		}
	}
	data.Set("hb_last_seen", jsonutils.NewTimeString(time.Now()))
	return data, nil
}

func (lbagent *SLoadbalancerAgent) GetExtraDetails(
	ctx context.Context,
	userCred mcclient.TokenCredential,
	query jsonutils.JSONObject,
	isList bool,
) (api.LoadbalancerAgentDetails, error) {
	return api.LoadbalancerAgentDetails{}, nil
}

func (manager *SLoadbalancerAgentManager) FetchCustomizeColumns(
	ctx context.Context,
	userCred mcclient.TokenCredential,
	query jsonutils.JSONObject,
	objs []interface{},
	fields stringutils2.SSortedStrings,
	isList bool,
) []api.LoadbalancerAgentDetails {
	rows := make([]api.LoadbalancerAgentDetails, len(objs))

	stdRows := manager.SStandaloneResourceBaseManager.FetchCustomizeColumns(ctx, userCred, query, objs, fields, isList)
	clusterRows := manager.SLoadbalancerClusterResourceBaseManager.FetchCustomizeColumns(ctx, userCred, query, objs, fields, isList)

	for i := range rows {
		rows[i] = api.LoadbalancerAgentDetails{
			StandaloneResourceDetails:       stdRows[i],
			LoadbalancerClusterResourceInfo: clusterRows[i],
		}
	}

	return rows
}

/*func (manager *SLoadbalancerAgentManager) QueryDistinctExtraField(q *sqlchemy.SQuery, field string) (*sqlchemy.SQuery, error) {
	var err error
	q, err = manager.SStandaloneResourceBaseManager.QueryDistinctExtraField(q, field)
	if err == nil {
		return q, nil
	}
	switch field {
	case "cluster":
		clusterQuery := LoadbalancerClusterManager.Query("name", "id").Distinct().SubQuery()
		q = q.Join(clusterQuery, sqlchemy.Equals(q.Field("cluster_id"), clusterQuery.Field("id")))
		q.GroupBy(clusterQuery.Field("name"))
		q.AppendField(clusterQuery.Field("name", "cluster"))
	default:
		return q, httperrors.NewBadRequestError("unsupport field %s", field)
	}
	return q, nil
}*/

func (manager *SLoadbalancerAgentManager) ListItemExportKeys(ctx context.Context,
	q *sqlchemy.SQuery,
	userCred mcclient.TokenCredential,
	keys stringutils2.SSortedStrings,
) (*sqlchemy.SQuery, error) {
	var err error

	q, err = manager.SStandaloneResourceBaseManager.ListItemExportKeys(ctx, q, userCred, keys)
	if err != nil {
		return nil, errors.Wrap(err, "SStandaloneResourceBaseManager.ListItemExportKeys")
	}
	if keys.ContainsAny(manager.SLoadbalancerClusterResourceBaseManager.GetExportKeys()...) {
		q, err = manager.SLoadbalancerClusterResourceBaseManager.ListItemExportKeys(ctx, q, userCred, keys)
		if err != nil {
			return nil, errors.Wrap(err, "SLoadbalancerClusterResourceBaseManager.ListItemExportKeys")
		}
	}

	return q, nil
}

func (man *SLoadbalancerAgentManager) getByClusterId(clusterId string) ([]SLoadbalancerAgent, error) {
	r := []SLoadbalancerAgent{}
	q := man.Query().Equals("cluster_id", clusterId)
	if err := db.FetchModelObjects(man, q, &r); err != nil {
		return nil, err
	}
	return r, nil
}

func (lbagent *SLoadbalancerAgent) AllowPerformHb(ctx context.Context, userCred mcclient.TokenCredential, query jsonutils.JSONObject, data *jsonutils.JSONDict) bool {
	return db.IsAdminAllowPerform(userCred, lbagent, "hb")
}

func (lbagent *SLoadbalancerAgent) PerformHb(ctx context.Context, userCred mcclient.TokenCredential, query jsonutils.JSONObject, data *jsonutils.JSONDict) (*jsonutils.JSONDict, error) {
	ipV := validators.NewIPv4AddrValidator("ip")
	haStateV := validators.NewStringChoicesValidator("ha_state", api.LB_HA_STATES)
	{
		keyV := map[string]validators.IValidator{
			"ip":       ipV,
			"ha_state": haStateV,
		}
		for _, v := range keyV {
			v.Optional(true)
			if err := v.Validate(data); err != nil {
				return nil, err
			}
		}
	}
	diff, err := lbagent.GetModelManager().TableSpec().Update(lbagent, func() error {
		lbagent.HbLastSeen = time.Now()
		if jVer, err := data.Get("version"); err == nil {
			if jVerStr, ok := jVer.(*jsonutils.JSONString); ok {
				lbagent.Version = jVerStr.Value()
			}
		}
		if ipV.IP != nil {
			lbagent.IP = ipV.IP.String()
		}
		if haStateV.Value != "" {
			lbagent.HaState = haStateV.Value
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(diff) > 1 {
		// other things changed besides hb_last_seen
		log.Infof("lbagent %s(%s) state changed: %s", lbagent.Name, lbagent.Id, diff)
		db.OpsLog.LogEvent(lbagent, db.ACT_UPDATE, diff, userCred)
	}
	return nil, nil
}

func (lbagent *SLoadbalancerAgent) IsActive() bool {
	if lbagent.HbLastSeen.IsZero() {
		return false
	}
	duration := time.Since(lbagent.HbLastSeen).Seconds()
	if int(duration) >= lbagent.HbTimeout {
		return false
	}
	return true
}

func (lbagent *SLoadbalancerAgent) AllowPerformParamsPatch(ctx context.Context, userCred mcclient.TokenCredential, query jsonutils.JSONObject, data *jsonutils.JSONDict) bool {
	return db.IsAdminAllowPerform(userCred, lbagent, "params-patch")
}

func (lbagent *SLoadbalancerAgent) PerformParamsPatch(ctx context.Context, userCred mcclient.TokenCredential, query jsonutils.JSONObject, data *jsonutils.JSONDict) (*jsonutils.JSONDict, error) {
	oldParams := lbagent.Params
	params := gotypes.DeepCopy(*lbagent.Params).(SLoadbalancerAgentParams)
	d := jsonutils.NewDict()
	d.Set("params", data)
	paramsV := validators.NewStructValidator("params", &params)
	if err := paramsV.Validate(d); err != nil {
		return nil, err
	}
	// new vrrp virtual_router_id should be unique across clusters
	if params.Vrrp.VirtualRouterId != oldParams.Vrrp.VirtualRouterId {
		clusterM, err := LoadbalancerClusterManager.FetchById(lbagent.ClusterId)
		if err != nil {
			return nil, httperrors.NewGeneralError(err)
		}
		cluster := clusterM.(*SLoadbalancerCluster)
		otherCluster, err := LoadbalancerClusterManager.findByVrrpRouterIdInZone(cluster.ZoneId, params.Vrrp.VirtualRouterId)
		if err != nil {
			return nil, err
		}
		if otherCluster != nil {
			return nil, httperrors.NewConflictError("lbcluster %s(%s) already has virtual_router_id %d",
				otherCluster.Name, otherCluster.Id, params.Vrrp.VirtualRouterId)
		}
	}
	// new vrrp priority should be unique in the cluster
	if params.Vrrp.Priority != oldParams.Vrrp.Priority {
		lbagents, err := LoadbalancerClusterManager.getLoadbalancerAgents(lbagent.ClusterId)
		if err != nil {
			return nil, httperrors.NewGeneralError(err)
		}
		for i := range lbagents {
			peerLbagent := &lbagents[i]
			if peerLbagent.Id == lbagent.Id {
				continue
			}
			if peerLbagent.Params.Vrrp.Priority == params.Vrrp.Priority {
				return nil, httperrors.NewConflictError("peer lbagent %s(%s) already has vrrp priority %d",
					peerLbagent.Name, peerLbagent.Id, params.Vrrp.Priority)
			}
		}
	}
	{
		diff, err := db.Update(lbagent, func() error {
			lbagent.Params = &params
			return nil
		})
		if err != nil {
			return nil, err
		}
		db.OpsLog.LogEvent(lbagent, db.ACT_UPDATE, diff, userCred)
	}
	if oldParams.Vrrp.needsUpdatePeer(&params.Vrrp) {
		lbagents, err := LoadbalancerClusterManager.getLoadbalancerAgents(lbagent.ClusterId)
		if err != nil {
			return nil, httperrors.NewGeneralError(err)
		}
		log.Infof("updating peer lbagents' vrrp params by those from %s(%s)", lbagent.Name, lbagent.Id)
		for i := range lbagents {
			peerLbagent := &lbagents[i]
			if lbagent.Id != peerLbagent.Id {
				diff, err := db.Update(peerLbagent, func() error {
					peerLbagent.Params.Vrrp.updateBy(&params.Vrrp)
					return nil
				})
				if err != nil {
					return nil, err
				}
				db.OpsLog.LogEvent(peerLbagent, db.ACT_UPDATE, diff, userCred)
			}
		}
	}
	return nil, nil
}

const (
	loadbalancerKeepalivedConfTmplDefault = `
global_defs {
	router_id {{ .agent.id }}
	#vrrp_strict
	vrrp_skip_check_adv_addr
	enable_script_security
}

vrrp_instance YunionLB {
	interface {{ .vrrp.interface }}
	virtual_router_id {{ .vrrp.virtual_router_id }}
	authentication {
		auth_type PASS
		auth_pass {{ .vrrp.pass }}
	}
	{{ if .vrrp.notify_script -}} notify {{ .vrrp.notify_script }} root {{- end }}
	{{ if .vrrp.unicast_peer -}} unicast_peer { {{- println }}
		{{- range .vrrp.unicast_peer }}		{{ println . }} {{- end }}
	}
	{{- end }}
	priority {{ .vrrp.priority }}
	advert_int {{ .vrrp.advert_int }}
	garp_master_refresh {{ .vrrp.garp_master_refresh }}
	{{ if .vrrp.preempt -}} preempt {{- else -}} nopreempt {{- end }}
	virtual_ipaddress {
		{{- printf "\n" }}
		{{- range .vrrp.addresses }}		{{ println . }} {{- end }}
		{{- printf "\t" -}}
	}
}
`
	loadbalancerHaproxyConfTmplDefault = `
global
	maxconn 20480
	tune.ssl.default-dh-param 2048
	{{- println }}
	{{- if .haproxy.tune_http_maxhdr }}	tune.http.maxhdr {{ println .haproxy.tune_http_maxhdr }} {{- end }}
	{{- if .haproxy.global_stats_socket }}	{{ println .haproxy.global_stats_socket }} {{- end }}
	{{- if .haproxy.global_nbthread }}	nbthread {{ println .haproxy.global_nbthread }} {{- end }}
	{{- if .haproxy.global_log }}	{{ println .haproxy.global_log }} {{- end }}

defaults
	timeout connect 10s
	timeout client 60s
	timeout server 60s
	timeout tunnel 1h
	{{- println }}
	{{- if .haproxy.global_log }}	{{ println "log global" }} {{- end }}
	{{- if not .haproxy.log_normal }}	{{ println "option dontlog-normal" }} {{- end }}

listen stats
	mode http
	bind :778
	stats enable
	stats hide-version
	stats realm "Haproxy Statistics"
	stats auth Yunion:LBStats
	stats uri /
`

	loadbalancerTelegrafConfTmplDefault = `
[[outputs.influxdb]]
	urls = ["{{ .telegraf.influx_db_output_url }}"]
	database = "{{ .telegraf.influx_db_output_name }}"
	insecure_skip_verify = {{ .telegraf.influx_db_output_unsafe_ssl }}

[[inputs.haproxy]]
	interval = "{{ .telegraf.haproxy_input_interval }}s"
	servers = ["{{ .telegraf.haproxy_input_stats_socket }}"]
	keep_field_names = true
`
)

var (
	loadbalancerKeepalivedConfTmplDefaultEncoded = base64.StdEncoding.EncodeToString([]byte(loadbalancerKeepalivedConfTmplDefault))
	loadbalancerHaproxyConfTmplDefaultEncoded    = base64.StdEncoding.EncodeToString([]byte(loadbalancerHaproxyConfTmplDefault))
	loadbalancerTelegrafConfTmplDefaultEncoded   = base64.StdEncoding.EncodeToString([]byte(loadbalancerTelegrafConfTmplDefault))
)
