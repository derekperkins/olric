// Copyright 2018-2021 Burak Sezer
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

package bufpool

import "testing"

func TestBufPool(t *testing.T) {
	p := New()
	b := make([]byte, 100)
	for i := 0; i < 1000; i++ {
		buf := p.Get()
		nr, err := buf.Write(b)
		if err != nil {
			t.Fatalf("Expected nil. Got: %v", err)
		}
		if nr != 100 {
			t.Fatalf("Expected 10. Got: %d", nr)
		}
		p.Put(buf)
	}
}
