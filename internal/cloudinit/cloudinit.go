/*
Copyright 2023.

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

package cloudinit

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// Very basic type definitions to generate cloud-init yaml

type CloudInit struct {
	Files   []File   `yaml:"write_files"`
	RunCmds []string `yaml:"runcmd"`
}

type File struct {
	Path        string `yaml:"path"`
	Content     string `yaml:"content"`
	Permissions string `yaml:"permissions"`
}

func (c *CloudInit) AsBytes() ([]byte, error) {
	var b bytes.Buffer

	// Write the "header" first
	_, err := b.WriteString("#cloud-config\n")
	if err != nil {
		return nil, err
	}
	// Marshal the data
	enc := yaml.NewEncoder(&b)
	defer enc.Close()

	err = enc.Encode(c)
	if err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}
