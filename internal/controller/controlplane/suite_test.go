/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controlplane

import (
	"os"
	"testing"

	"github.com/k0smotron/k0smotron/internal/test/envtest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var (
	testEnv             *envtest.Environment
	secretCachingClient client.Client
	ctx                 = ctrl.SetupSignalHandler()
)

func TestMain(m *testing.M) {
	setupSecretCachedClient := func(mgr manager.Manager) error {
		var err error
		secretCachingClient, err = client.New(mgr.GetConfig(), client.Options{
			HTTPClient: mgr.GetHTTPClient(),
			Cache: &client.CacheOptions{
				Reader: mgr.GetCache(),
			},
		})
		if err != nil {
			return err
		}

		return nil
	}
	testEnv = envtest.Build(ctx, setupSecretCachedClient)
	code := m.Run()
	testEnv.Teardown()
	os.Exit(code)
}
