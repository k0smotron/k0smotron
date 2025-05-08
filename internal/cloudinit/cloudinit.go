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
	"strconv"

	"gopkg.in/yaml.v3"
)

// Very basic type definitions to generate cloud-init yaml

type CloudInit struct {
	Files           []File   `yaml:"write_files,omitempty" json:"write_files,omitempty"`
	RunCmds         []string `yaml:"runcmd" json:"runcmd,omitempty"`
	CustomCloudInit string   `yaml:"-" json:"-,omitempty"`
}

type File struct {
	Path        string `yaml:"path" json:"path,omitempty"`
	Content     string `yaml:"content" json:"content,omitempty"`
	Permissions string `yaml:"permissions" json:"permissions,omitempty"`
}

func (c *CloudInit) AsBytes() ([]byte, error) {
	var b bytes.Buffer

	// Write the "header" first
	_, err := b.WriteString("## template: jinja\n")
	if err != nil {
		return nil, err
	}
	_, err = b.WriteString("#cloud-config\n")
	if err != nil {
		return nil, err
	}
	// Marshal the data
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	defer enc.Close()

	err = enc.Encode(c)
	if err != nil {
		return nil, err
	}

	if c.CustomCloudInit != "" {
		_, err = b.WriteString("\n#cloud-config\n")
		if err != nil {
			return nil, err
		}
		_, err = b.WriteString(c.CustomCloudInit)
		if err != nil {
			return nil, err
		}
	}

	return b.Bytes(), nil
}

func (f File) PermissionsAsInt() (int64, error) {

	return strconv.ParseInt(f.Permissions, 8, 32)
}
