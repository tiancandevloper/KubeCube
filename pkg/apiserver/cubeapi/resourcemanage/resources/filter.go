/*
Copyright 2021 KubeCube Authors

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

package resources

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io/ioutil"
	"net/http"
	"sort"
	"strings"
	"time"

	jsoniter "github.com/json-iterator/go"
	"github.com/kubecube-io/kubecube/pkg/clog"
)

const (
	Labels      = "labels"
	Annotations = "annotations"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type Filter struct {
	Exact     map[string]string
	Fuzzy     map[string]string
	Limit     int
	Offset    int
	SortName  string
	SortOrder string
	SortFunc  string
}

type K8sJson = map[string]interface{}
type K8sJsonArr = []interface{}

// modify the response
func (f *Filter) ModifyResponse(r *http.Response) error {
	// get info from response
	var body []byte
	var err error
	codeType := r.Header.Get("Content-Encoding")
	switch codeType {
	case "gzip":
		reader, err := gzip.NewReader(r.Body)
		defer reader.Close()
		defer r.Body.Close()
		if err != nil {
			clog.Info("can not read gzip body from response, %v", err)
			return err
		}
		body, err = ioutil.ReadAll(reader)
		if err != nil {
			clog.Info("can not read gzip body from response, %v", err)
			return err
		}
	default:
		body, err = ioutil.ReadAll(r.Body)
		defer r.Body.Close()
		if err != nil {
			clog.Info("can not read body from response, %v", err)
			return err
		}
	}

	// filter result
	result := f.FilterResult(body)
	if result == nil {
		return fmt.Errorf("filter the k8s response body fail")
	}

	// return result
	buf := bytes.NewBuffer(result)
	r.Body = ioutil.NopCloser(buf)
	r.Header["Content-Length"] = []string{fmt.Sprint(buf.Len())}
	delete(r.Header, "Content-Encoding")
	return nil
}

// filter result by exact/fuzzy match, sort, page
func (f *Filter) FilterResult(body []byte) []byte {
	var result K8sJson
	err := json.Unmarshal(body, &result)
	if err != nil {
		clog.Warn("can not parser body to map, %v ", err)
		return nil
	}

	// list type
	if result["items"] != nil {
		if items, ok := result["items"].(K8sJsonArr); ok {
			// match selector
			items = f.exactMatch(items)
			items = f.fuzzyMatch(items)
			result["total"] = len(items)
			// sort
			items = f.sort(items)
			// page
			items = f.page(items)
			result["items"] = items
		}
	}

	resultJson, err := json.Marshal(result)
	if err != nil {
		clog.Info("translate modify response result to json fail, %v", err)
		return nil
	}
	return resultJson
}

// filter result by exact/fuzzy match, sort, page
func (f *Filter) FilterResultToMap(body []byte) K8sJson {
	var result K8sJson
	err := json.Unmarshal(body, &result)
	if err != nil {
		clog.Error("can not parser body to map, %v ", err)
		return nil
	}

	// list type
	if result["items"] != nil {
		if items, ok := result["items"].(K8sJsonArr); ok {
			// match selector
			items = f.exactMatch(items)
			items = f.fuzzyMatch(items)
			result["total"] = len(items)
			// sort
			items = f.sort(items)
			// page
			items = f.page(items)
			result["items"] = items
		}
	}

	return result
}

// exact search
func (f *Filter) exactMatch(items K8sJsonArr) (result K8sJsonArr) {
	if len(f.Exact) == 0 {
		return items
	}

	// every list record
	for _, item := range items {
		flag := true
		// every exact match condition
		for key, value := range f.Exact {
			// key = .metadata.xxx.xxx， multi level
			realValue := GetDeepValue(item, key)
			// if one condition not match
			if !strings.EqualFold(realValue, value) {
				flag = false
				break
			}
		}
		// if every exact condition match
		if flag {
			result = append(result, item)
		}
	}
	return
}

// fuzzy search
func (f *Filter) fuzzyMatch(items K8sJsonArr) (result K8sJsonArr) {
	if len(f.Fuzzy) == 0 {
		return items
	}

	// every list record
	for _, item := range items {
		flag := true
		// every fuzzy match condition
		for key, value := range f.Fuzzy {
			// key = metadata.xxx.xxx， multi level
			realValue := GetDeepValue(item, key)
			// if one condition not match
			if !strings.Contains(realValue, value) {
				flag = false
				break
			}
		}
		// if every fuzzy condition match
		if flag {
			result = append(result, item)
		}
	}
	return
}

// sort by .metadata.name/.metadata.creationTimestamp
func (f *Filter) sort(items K8sJsonArr) K8sJsonArr {
	if len(items) == 0 {
		return items
	}

	sort.Slice(items, func(i, j int) bool {
		switch f.SortFunc {
		case "string":
			si := GetDeepValue(items[i], f.SortName)
			sj := GetDeepValue(items[j], f.SortName)
			if f.SortOrder == "asc" {
				return strings.Compare(si, sj) < 0
			} else {
				return strings.Compare(si, sj) > 0
			}
		case "time":
			ti, err := time.Parse("2006-01-02T15:04:05Z", GetDeepValue(items[i], f.SortName))
			if err != nil {
				return false
			}
			tj, err := time.Parse("2006-01-02T15:04:05Z", GetDeepValue(items[j], f.SortName))
			if err != nil {
				return false
			} else if f.SortOrder == "asc" {
				return ti.Before(tj)
			} else {
				return ti.After(tj)
			}
		case "number":
			ni := GetDeepFloat64(items[i], f.SortName)
			nj := GetDeepFloat64(items[j], f.SortName)
			if f.SortOrder == "asc" {
				return ni < nj
			} else if f.SortOrder == "desc" {
				return ni > nj
			} else {
				return ni < nj
			}
		default:
			si := GetDeepValue(items[i], f.SortName)
			sj := GetDeepValue(items[j], f.SortName)
			if f.SortOrder == "asc" {
				return strings.Compare(si, sj) < 0
			} else {
				return strings.Compare(si, sj) > 0
			}
		}

	})
	return items
}

// page
func (f *Filter) page(items K8sJsonArr) K8sJsonArr {
	if len(items) == 0 {
		return items
	}

	size := len(items)
	if f.Offset >= size {
		return items[0:0]
	}
	end := f.Offset + f.Limit
	if end > size {
		end = size
	}
	return items[f.Offset:end]
}

// get value by metadata.xx.xx.xx, multi level key
func GetDeepValue(item interface{}, keyStr string) (value string) {
	defer func() {
		if err := recover(); err != nil {
			value = ""
			return
		}
	}()

	info := item.(K8sJson)
	// key = metadata.xxx.xxx， multi level
	keys := strings.Split(keyStr, ".")
	n := len(keys)
	i := 0
	for ; n > 0 && i < n-1; i++ {
		temp, ok := info[keys[i]].(K8sJson)
		if !ok {
			temp = info[keys[i]].(K8sJsonArr)[0].(K8sJson)
		}
		info = temp

		if keys[i] == Labels || keys[i] == Annotations {
			i++
			break
		}
	}
	key := strings.Join(keys[i:], ".")
	value = info[key].(string)
	return
}

// get float64 value by metadata.xx.xx.xx, multi level key
func GetDeepFloat64(item interface{}, keyStr string) (value float64) {
	defer func() {
		if err := recover(); err != nil {
			value = 0
			return
		}
	}()

	temp := item.(K8sJson)
	// key = metadata.xxx.xxx， multi level
	keys := strings.Split(keyStr, ".")
	n := len(keys)
	i := 0
	for ; n > 0 && i < n-1; i++ {
		temp = temp[keys[i]].(K8sJson)
		if keys[i] == Labels || keys[i] == Annotations {
			i++
			break
		}
	}
	key := strings.Join(keys[i:], ".")
	value = temp[key].(float64)
	return
}

// get map by spec.selector.matchLabels={xx= xx}
func GetDeepMap(item interface{}, keyStr string) (value K8sJson) {
	defer func() {
		if err := recover(); err != nil {
			value = nil
			return
		}
	}()

	temp := item.(K8sJson)
	// key = spec.selector.matchLabels， multi level
	keys := strings.Split(keyStr, ".")
	n := len(keys)
	i := 0
	for ; n > 0 && i < n-1; i++ {
		temp = temp[keys[i]].(K8sJson)
		if keys[i] == Labels || keys[i] == Annotations {
			i++
			break
		}
	}
	key := strings.Join(keys[i:], ".")
	value = temp[key].(K8sJson)
	return
}

// get metadata.ownerReference[0]
func GetDeepArray(item interface{}, keyStr string) (value K8sJsonArr) {
	defer func() {
		if err := recover(); err != nil {
			value = nil
			return
		}
	}()

	temp := item.(K8sJson)
	// key = metadata.ownerReference[0]， multi level
	keys := strings.Split(keyStr, ".")
	n := len(keys)
	i := 0
	for ; n > 0 && i < n-1; i++ {
		temp = temp[keys[i]].(K8sJson)
		if keys[i] == Labels || keys[i] == Annotations {
			i++
			break
		}
	}
	key := strings.Join(keys[i:], ".")
	value = temp[key].(K8sJsonArr)
	return
}
