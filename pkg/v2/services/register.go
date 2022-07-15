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

package services

import (
	"context"
	"net/http"

	restful "github.com/emicklei/go-restful/v3"
	"kubegems.io/kubegems/pkg/log"
	"kubegems.io/kubegems/pkg/utils/route"
	"kubegems.io/kubegems/pkg/utils/system"
	applicationhandler "kubegems.io/kubegems/pkg/v2/services/handlers/application"
	approvehandler "kubegems.io/kubegems/pkg/v2/services/handlers/approve"
	appstorehandler "kubegems.io/kubegems/pkg/v2/services/handlers/appstore"
	"kubegems.io/kubegems/pkg/v2/services/handlers/base"
	clusterhandler "kubegems.io/kubegems/pkg/v2/services/handlers/clusters"
	loginhandler "kubegems.io/kubegems/pkg/v2/services/handlers/login"
	tenanthandler "kubegems.io/kubegems/pkg/v2/services/handlers/tenants"
	userhandler "kubegems.io/kubegems/pkg/v2/services/handlers/users"
	"kubegems.io/kubegems/pkg/v2/services/options"
)

type RestHandler interface {
	Regist(c *restful.Container)
}

func NewRest(deps *Dependencies, opts *options.Options) *restful.Container {
	base := base.NewBaseHandler(deps.Agentscli, deps.Redis, deps.Databse)
	handlers := []RestHandler{
		&loginhandler.Handler{
			BaseHandler: base,
			JWTOptions:  opts.JWT,
		},
		&userhandler.Handler{
			BaseHandler: base,
		},
		&clusterhandler.Handler{
			BaseHandler: base,
		},
		&tenanthandler.Handler{
			BaseHandler: base,
		},
		&approvehandler.Handler{
			BaseHandler: base,
		},
		appstorehandler.MustNewHandler(base, opts.Appstore),
		applicationhandler.MustNewApplicationDeployHandler(base, opts.Git, deps.Argocli),
	}

	// register handlers
	c := restful.NewContainer()
	for _, handler := range handlers {
		handler.Regist(c)
	}

	// enableSwagger
	c.Add(route.BuildOpenAPIWebService(c.RegisteredWebServices(), "docs.json", enrichSwaggerObject))

	enableFilters(c, deps.Databse.DB(), opts)
	return c
}

func RunRest(ctx context.Context, opts *system.Options, handler http.Handler) error {
	log.FromContextOrDiscard(ctx).Info("rest server listening on", "address", opts.Listen)
	server := &http.Server{
		Addr:    opts.Listen,
		Handler: handler,
	}
	go func() {
		<-ctx.Done()
		server.Shutdown(ctx)
		server.Close()
	}()
	return server.ListenAndServe()
}
