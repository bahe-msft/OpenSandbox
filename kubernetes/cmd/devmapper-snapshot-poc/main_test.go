// Copyright 2026 Alibaba Group Holding Ltd.
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

package main

import "testing"

func TestParseThinDeviceID(t *testing.T) {
	id, err := parseThinDeviceID("0 524288000 thin /dev/mapper/containerd-thinpool 176")
	if err != nil {
		t.Fatal(err)
	}
	if id != 176 {
		t.Fatalf("got %d, want 176", id)
	}
}

func TestParseThinDeviceIDRejectsUnexpectedTable(t *testing.T) {
	if _, err := parseThinDeviceID("0 1024 linear /dev/sda 0"); err == nil {
		t.Fatal("expected an error")
	}
}
