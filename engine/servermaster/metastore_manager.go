// Copyright 2022 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package servermaster

import (
	"sync"

	"github.com/pingcap/log"
	"github.com/pingcap/tiflow/engine/pkg/meta/metaclient"
	"github.com/pingcap/tiflow/pkg/errors"
	"go.uber.org/zap"
)

// MetaStoreManager defines an interface to manage metastore
type MetaStoreManager interface {
	// Register register specify backend store to manager with an unique id
	// id can be some readable identifier, like `meta-test1`.
	// Duplicate id will return an error
	Register(id string, store *metaclient.StoreConfig) error
	// UnRegister delete an existing backend store
	UnRegister(id string)
	// GetMetaStore get an existing backend store info
	GetMetaStore(id string) *metaclient.StoreConfig
}

type metaStoreManagerImpl struct {
	// From id to metaclient.StoreConfig
	id2Store sync.Map
}

// NewMetaStoreManager creates a new metaStoreManagerImpl instance
func NewMetaStoreManager() MetaStoreManager {
	return &metaStoreManagerImpl{}
}

// Register implements MetaStoreManager.Register
func (m *metaStoreManagerImpl) Register(id string, store *metaclient.StoreConfig) error {
	if _, exists := m.id2Store.LoadOrStore(id, store); exists {
		log.Error("register metastore fail", zap.Any("config", store), zap.String("err", "Duplicate storeID"))
		return errors.ErrMetaStoreIDDuplicate.GenWithStackByArgs()
	}
	return nil
}

// Unregister implements MetaStoreManager.Unregister
func (m *metaStoreManagerImpl) UnRegister(id string) {
	m.id2Store.Delete(id)
	log.Info("unregister metastore", zap.String("storeID", id))
}

// GetMetaStore implements MetaStoreManager.GetMetaStore
func (m *metaStoreManagerImpl) GetMetaStore(id string) *metaclient.StoreConfig {
	if store, exists := m.id2Store.Load(id); exists {
		return store.(*metaclient.StoreConfig)
	}

	return nil
}
