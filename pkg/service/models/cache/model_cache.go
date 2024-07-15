// Copyright 2022 The kubegems.io Authors
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

package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
	"kubegems.io/kubegems/pkg/log"
	"kubegems.io/kubegems/pkg/service/models"
	"kubegems.io/kubegems/pkg/utils/redis"
)

/*
出于权限判断，审计获取信息等功能考虑，需要缓存全局的 租户，项目，环境等 结构
基于redis的缓存树设计
hashset {
	tenant_1: {
		id: 1,
		kind: tenant,
		name: tenant1,
		children: [proj_2]
	},
	proj_2: {
		id: 2,
		kind: project,
		name: project2,
		children: [app_1, env_1]
	},
	env_1: {
		id: 1,
		kind: env,
		name: environment1,
		children: [app_1]
	},
	app_1: {
		id: 1,
		kind: env,
		name: application1,
		children: [env_1]
	}
}
*/

const (
	// 全局模型结构缓存
	ModelCacheKey = "_model_cache"
	// 用户登录过期时间(minute)
	userAuthorizationDataExpireMinute = 180
)

// nolint
type ModelCache interface {
	BuildCacheIfNotExist() error
	UpsertTenant(tid uint, name string) error
	DelTenant(tid uint) error
	UpsertProject(tid, pid uint, name string) error
	DelProject(tid, pid uint) error

	UpsertEnvironment(pid, eid uint, name, cluster, namespace string) error
	DelEnvironment(pid, eid uint, cluster, namespace string) error
	FindEnvironment(cluster, namespace string) CommonResourceIface

	UpsertVirtualSpace(vid uint, name string) error
	DelVirtualSpace(vid uint) error

	GetUserAuthority(user models.CommonUserIface) *UserAuthority
	FlushUserAuthority(user models.CommonUserIface) *UserAuthority

	FindParents(kind string, id uint) []CommonResourceIface
	FindResource(kind string, id uint) CommonResourceIface
}

func NewRedisModelCache(db *gorm.DB, redis *redis.Client) ModelCache {
	return RedisModelCache{DB: db, Redis: redis}
}

type RedisModelCache struct {
	DB    *gorm.DB
	Redis *redis.Client
}

func (t RedisModelCache) BuildCacheIfNotExist() error {
	r, err := t.Redis.Exists(context.Background(), ModelCacheKey).Result()
	if err != nil {
		return err
	}
	if r > 0 {
		return nil
	}
	tenants := []models.Tenant{}
	if err := t.DB.Find(&tenants).Error; err != nil {
		return err
	}
	dataMap := make(map[string]interface{})
	for _, tenant := range tenants {
		n := &Entity{Name: tenant.TenantName, Kind: models.ResTenant, ID: tenant.ID}
		dataMap[n.cacheKey()] = n
	}

	projects := []models.Project{}
	if err := t.DB.Find(&projects).Error; err != nil {
		return err
	}
	for _, project := range projects {
		n := &Entity{Name: project.ProjectName, Kind: models.ResProject, ID: project.ID, Owner: []*Entity{{Kind: models.ResTenant, ID: project.TenantID}}}
		dataMap[n.cacheKey()] = n
	}

	envs := []models.Environment{}
	if err := t.DB.Preload("Cluster").Find(&envs).Error; err != nil {
		return err
	}
	for _, env := range envs {
		if env.Cluster == nil {
			continue
		}
		n := &Entity{
			Name:      env.EnvironmentName,
			Kind:      models.ResEnvironment,
			ID:        env.ID,
			Namespace: env.Namespace,
			Cluster:   env.Cluster.ClusterName,
			Owner:     []*Entity{{Kind: models.ResProject, ID: env.ProjectID}},
		}
		dataMap[n.cacheKey()] = n
		dataMap[envCacheKey(n.Cluster, n.Namespace)] = n
	}
	vspaces := []models.VirtualSpace{}
	if err := t.DB.Find(&vspaces).Error; err != nil {
		return err
	}
	for _, vspace := range vspaces {
		n := &Entity{Name: vspace.VirtualSpaceName, Kind: models.ResVirtualSpace, ID: vspace.ID, Owner: []*Entity{}}
		dataMap[n.cacheKey()] = n
	}
	if len(dataMap) == 0 {
		log.Info("empty cache data")
		return nil
	}
	if _, err := t.Redis.HSet(context.Background(), ModelCacheKey, dataMap).Result(); err != nil {
		log.Error(err, "failed to rebuild cache", "datamap", dataMap)
		return err
	}
	return nil
}

func (t RedisModelCache) UpsertTenant(tid uint, name string) error {
	n := Entity{Name: name, Kind: models.ResTenant, ID: tid}
	_, err := t.Redis.HSet(context.Background(), ModelCacheKey, n.toPair()).Result()
	if err != nil {
		log.Error(err, "cache upsert tenant failed", "tenant_id", tid, "tenant_name", name)
	}
	return err
}

func (t RedisModelCache) DelTenant(tid uint) error {
	_, err := t.Redis.HDel(context.Background(), ModelCacheKey, cacheKey(models.ResTenant, tid)).Result()
	if err != nil {
		log.Error(err, "cache delete tenant failed", "tenant_id", tid)
	}
	return err
}

func (t RedisModelCache) UpsertProject(tid, pid uint, name string) error {
	n := Entity{Name: name, Kind: models.ResProject, ID: pid, Owner: []*Entity{{Kind: models.ResTenant, ID: tid}}}
	_, err := t.Redis.HSet(context.Background(), ModelCacheKey, n.toPair()).Result()
	if err != nil {
		log.Error(err, "cache upsert project failed", "tenant_id", tid, "project_id", pid, "project_name", name)
	}
	return err
}

func (t RedisModelCache) DelProject(tid, pid uint) error {
	_, err := t.Redis.HDel(context.Background(), ModelCacheKey, cacheKey(models.ResProject, pid)).Result()
	if err != nil {
		log.Error(err, "cache delete project failed", "tenant_id", tid, "project_id", pid)
	}
	return err
}

func (t RedisModelCache) UpsertEnvironment(pid, eid uint, name, cluster, namespace string) error {
	n := Entity{Name: name, Kind: models.ResEnvironment, ID: eid, Namespace: namespace, Cluster: cluster, Owner: []*Entity{{Kind: models.ResProject, ID: pid}}}
	ctx := context.Background()
	_, err1 := t.Redis.HSet(ctx, ModelCacheKey, n.toPair()).Result()
	if err1 != nil {
		log.Error(err1, "cache upsert environment 1 failed", "project_id", pid, "environment_id", eid, "cluster", cluster, "namespace", namespace)
		return err1
	}
	_, err2 := t.Redis.HSet(ctx, ModelCacheKey, n.toEnvPair()).Result()
	if err2 != nil {
		log.Error(err2, "cache upsert environment 2 failed", "project_id", pid, "environment_id", eid, "cluster", cluster, "namespace", namespace)
		return err2
	}
	return nil
}

func (t RedisModelCache) DelEnvironment(pid, eid uint, cluster, namespace string) error {
	_, err := t.Redis.HDel(context.Background(), ModelCacheKey, cacheKey(models.ResEnvironment, eid)).Result()
	if err != nil {
		log.Error(err, "cache delete environment 1 failed", "project_id", pid, "environment_id", eid)
		return err
	}
	_, err2 := t.Redis.HDel(context.Background(), ModelCacheKey, envCacheKey(cluster, namespace)).Result()
	if err2 != nil {
		log.Error(err2, "cache delete environment 2 failed", "project_id", pid, "environment_id", eid)
		return err2
	}
	return nil
}

func (t RedisModelCache) UpsertVirtualSpace(vid uint, name string) error {
	_, err := t.Redis.HSet(context.Background(), ModelCacheKey, cacheKey(models.ResVirtualSpace, vid)).Result()
	if err != nil {
		log.Error(err, "cache upsert virtualspace failed", "vid", vid, "name", name)
		return err
	}
	return err
}

func (t RedisModelCache) DelVirtualSpace(vid uint) error {
	_, err := t.Redis.HDel(context.Background(), ModelCacheKey, cacheKey(models.ResVirtualSpace, vid)).Result()
	if err != nil {
		log.Error(err, "cache delete virtualspace failed", "vid", vid)
		return err
	}
	return err
}

func (c RedisModelCache) FindParents(kind string, id uint) []CommonResourceIface {
	var ret []CommonResourceIface
	parentsRaw, err := c.Redis.Eval(context.Background(), FindParentScript, []string{ModelCacheKey, kind, strconv.FormatUint(uint64(id), 10)}).Result()
	if err != nil {
		log.Error(err, "failed to eval lua script", "script_name", "FindParentScript")
		return nil
	}
	rawStringArr := parentsRaw.([]interface{})
	for idx := range rawStringArr {
		entity := Entity{}
		s := rawStringArr[idx].(string)
		if err := json.Unmarshal([]byte(s), &entity); err != nil {
			log.Error(err, "failed to unmarshal Entity", "raw", s)
			continue
		}
		ret = append(ret, &entity)
	}
	return ret
}

func (c RedisModelCache) FindResource(kind string, id uint) CommonResourceIface {
	key := cacheKey(kind, id)
	var ret CommonResourceIface
	var e Entity
	if err := c.Redis.HGet(context.Background(), ModelCacheKey, key).Scan(&e); err != nil {
		return nil
	}
	ret = &e
	return ret
}

func (c RedisModelCache) FindEnvironment(cluster, namespace string) CommonResourceIface {
	var e Entity
	if err := c.Redis.HGet(context.Background(), ModelCacheKey, envCacheKey(cluster, namespace)).Scan(&e); err != nil {
		return nil
	}
	return &e
}

func userAuthorityKey(username string) string {
	return fmt.Sprintf("user_authority_data__%s", username)
}

func (c RedisModelCache) GetUserAuthority(user models.CommonUserIface) *UserAuthority {
	var authinfo UserAuthority
	err := c.Redis.Get(context.Background(), userAuthorityKey(user.GetUsername())).Scan(&authinfo)
	if err != nil {
		log.Error(err, "failed to get user authority from cache, will flush new one", "user", user.GetUsername())
		newAuthInfo := c.FlushUserAuthority(user)
		return newAuthInfo
	}
	return &authinfo
}

func (c RedisModelCache) FlushUserAuthority(user models.CommonUserIface) *UserAuthority {
	auth := new(UserAuthority)
	sysrole := models.SystemRole{ID: user.GetSystemRoleID()}

	if err := c.DB.First(&sysrole).Error; err != nil {
		log.Error(err, "faield to get user system role", "user", user.GetUsername())
	}

	var turs []models.TenantUserRels
	if err := c.DB.Preload("Tenant").Find(&turs, "user_id = ?", user.GetID()).Error; err != nil {
		log.Error(err, "faield to get user tenantlist", "user", user.GetUsername())
	}
	var purs []models.ProjectUserRels
	if err := c.DB.Preload("Project").Find(&purs, "user_id = ?", user.GetID()).Error; err != nil {
		log.Error(err, "faield to get user projectlist", "user", user.GetUsername())
	}

	var eurs []models.EnvironmentUserRels
	if err := c.DB.Preload("Environment").Find(&eurs, "user_id = ?", user.GetID()).Error; err != nil {
		log.Error(err, "faield to get user environmentlist", "user", user.GetUsername())
	}

	var vurs []models.VirtualSpaceUserRels
	if err := c.DB.Preload("VirtualSpace").Find(&vurs, "user_id = ?", user.GetID()).Error; err != nil {
		log.Error(err, "faield to get user virtualspacelist", "user", user.GetUsername())
	}

	auth.SystemRole = sysrole.RoleCode
	auth.Tenants = make([]*UserResource, len(turs))
	auth.Projects = make([]*UserResource, len(purs))
	auth.Environments = make([]*UserResource, len(eurs))
	auth.VirtualSpaces = make([]*UserResource, len(vurs))

	for i := range turs {
		auth.Tenants[i] = &UserResource{
			ID:      int(turs[i].TenantID),
			Name:    turs[i].Tenant.TenantName,
			Role:    turs[i].Role,
			IsAdmin: turs[i].Role == models.TenantRoleAdmin,
		}
	}
	for i := range purs {
		auth.Projects[i] = &UserResource{
			ID:      int(purs[i].ProjectID),
			Name:    purs[i].Project.ProjectName,
			Role:    purs[i].Role,
			IsAdmin: purs[i].Role == models.ProjectRoleAdmin,
		}
	}
	for i := range eurs {
		auth.Environments[i] = &UserResource{
			ID:      int(eurs[i].EnvironmentID),
			Name:    eurs[i].Environment.EnvironmentName,
			Role:    eurs[i].Role,
			IsAdmin: eurs[i].Role == models.EnvironmentRoleOperator,
		}
	}
	for i := range vurs {
		auth.VirtualSpaces[i] = &UserResource{
			ID:      int(vurs[i].VirtualSpaceID),
			Name:    vurs[i].VirtualSpace.VirtualSpaceName,
			Role:    vurs[i].Role,
			IsAdmin: vurs[i].Role == models.VirtualSpaceRoleAdmin,
		}
	}

	if _, err := c.Redis.Set(context.Background(), userAuthorityKey(user.GetUsername()), auth, time.Duration(userAuthorizationDataExpireMinute)*time.Minute).Result(); err != nil {
		log.Error(err, "failed to cache user authority")
	}
	return auth
}
