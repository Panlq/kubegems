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

package worker

import (
	"context"
	"net/http"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	"kubegems.io/kubegems/pkg/log"
	"kubegems.io/kubegems/pkg/utils/agents"
	"kubegems.io/kubegems/pkg/utils/argo"
	"kubegems.io/kubegems/pkg/utils/database"
	"kubegems.io/kubegems/pkg/utils/git"
	_ "kubegems.io/kubegems/pkg/utils/kube"
	"kubegems.io/kubegems/pkg/utils/pprof"
	"kubegems.io/kubegems/pkg/utils/prometheus/exporter"
	"kubegems.io/kubegems/pkg/utils/redis"
	"kubegems.io/kubegems/pkg/worker/dump"
	"kubegems.io/kubegems/pkg/worker/resourcelist"
	"kubegems.io/kubegems/pkg/worker/task"
)

type Dependencies struct {
	Redis     *redis.Client
	Databse   *database.Database
	Argocli   *argo.Client
	Git       git.Provider
	Agentscli *agents.ClientSet
	Logger    logr.Logger
}

func prepareDependencies(ctx context.Context, options *Options) (*Dependencies, error) {
	// logger
	log.SetLevel(options.LogLevel)

	// redis
	var rediscli *redis.Client
	if options.Redis.Addr != "" {
		newrediscli, err := redis.NewClient(options.Redis)
		if err != nil {
			return nil, err
		}
		rediscli = newrediscli
	} else {
		log.Info("redis not configured")
	}
	// database
	databasecli, err := database.NewDatabase(options.Mysql)
	if err != nil {
		return nil, err
	}
	// agent client
	agentclientset, err := agents.NewClientSet(databasecli)
	if err != nil {
		return nil, err
	}
	// git
	gitprovider := git.NewLazyProvider(options.Git)
	// argo
	argocli := argo.NewLazyClient(ctx, options.Argo)

	return &Dependencies{
		Redis:     rediscli,
		Databse:   databasecli,
		Argocli:   argocli,
		Git:       gitprovider,
		Agentscli: agentclientset,
	}, nil
}

func Run(ctx context.Context, options *Options) error {
	ctx = logr.NewContext(ctx, log.LogrLogger)
	deps, err := prepareDependencies(ctx, options)
	if err != nil {
		return err
	}

	// dump
	dump := &dump.Dump{Options: options.Dump, DB: deps.Databse}
	dump.Start()

	// resource cache
	cache := resourcelist.NewResourceCache(deps.Databse, deps.Agentscli)
	cache.Start()

	http.HandleFunc("/refresh", func(w http.ResponseWriter, _ *http.Request) {
		if err := cache.EnvironmentSync(); err != nil {
			w.Write([]byte(err.Error()))
		} else {
			w.Write([]byte("ok"))
		}
		if err := cache.WorkloadSync(); err != nil {
			w.Write([]byte(err.Error()))
		} else {
			w.Write([]byte("ok"))
		}
	})

	exporterHandler := exporter.NewHandler("gems_worker", map[string]exporter.Collectorfunc{})

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return pprof.Run(ctx)
	})
	eg.Go(func() error {
		return exporterHandler.Run(ctx, options.Exporter)
	})
	eg.Go(func() error {
		return task.Run(ctx, options.Listen, deps.Redis, deps.Databse, deps.Git, deps.Argocli, options.AppStore, deps.Agentscli)
	})
	return eg.Wait()
}
