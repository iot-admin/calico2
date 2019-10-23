// Copyright (c) 2019 Tigera, Inc. All rights reserved.
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

package bpf

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type Map interface {
	EnsureExists() error
	Iter(func(k, v []byte)) error
	Update(k, v []byte) error
	Delete(k []byte) error
}

func NewPinnedMap(name, filename string, mapType string, keySize, valueSize int, maxEntries int, flags int) Map {
	m := &PinnedMap{
		Filename:   filename,
		Type:       mapType,
		KeySize:    keySize,
		ValueSize:  valueSize,
		MaxEntries: maxEntries,
		Name:       name,
		Flags:      flags,
	}
	return m
}

type PinnedMap struct {
	Filename   string
	Type       string
	KeySize    int
	ValueSize  int
	MaxEntries int
	Name       string
	Flags      int

	fdLoaded bool
	fd       MapFD
}

func (b *PinnedMap) Close() error {
	err := b.fd.Close()
	b.fdLoaded = false
	b.fd = 0
	return err
}

func (b *PinnedMap) Iter(f func(k, v []byte)) error {
	prog := "bpftool"
	args := []string{
		"--json",
		"--pretty",
		"map",
		"dump",
		"pinned",
		b.Filename,
	}

	printCommand(prog, args...)
	output, err := exec.Command(prog, args...).CombinedOutput()
	if err != nil {
		return errors.Errorf("failed to dump in map (%s): %s\n%s", b.Filename, err, output)
	}

	var mp []mapEntry
	err = json.Unmarshal(output, &mp)
	if err != nil {
		return errors.Errorf("cannot parse json output: %v\n%s", err, output)
	}

	for _, me := range mp {
		k, err := hexStringsToBytes(me.Key)
		if err != nil {
			return errors.Errorf("failed parsing map %s entry %s key: %e", b.Filename, me, err)
		}
		v, err := hexStringsToBytes(me.Value)
		if err != nil {
			return errors.Errorf("failed parsing map %s entry %s val: %e", b.Filename, me, err)
		}
		f(k, v)
	}

	return nil
}

func (b *PinnedMap) Update(k, v []byte) error {
	return UpdateMapEntry(b.fd, k, v)
}

func appendBytes(strings []string, bytes []byte) []string {
	for _, b := range bytes {
		strings = append(strings, strconv.FormatInt(int64(b), 10))
	}
	return strings
}

func (b *PinnedMap) Delete(k []byte) error {
	args := make([]string, 0, 10+len(k))
	args = append(args, "map", "delete",
		"pinned", b.Filename,
		"key")
	args = appendBytes(args, k)

	cmd := exec.Command("bpftool", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		logrus.WithField("out", string(out)).Error("Failed to run bpftool")
	}
	return err
}

func (b *PinnedMap) EnsureExists() error {
	if b.fdLoaded {
		return nil
	}

	_, err := maybeMountBPFfs()
	if err != nil {
		logrus.WithError(err).Error("Failed to mount bpffs")
		return err
	}
	// FIXME hard-coded dir
	err = os.MkdirAll("/sys/fs/bpf/tc/globals", 0700)
	if err != nil {
		logrus.WithError(err).Error("Failed create dir")
		return err
	}
	_, err = os.Stat(b.Filename)
	if err == nil {
		b.fd, err = GetPinnedMapFD(b.Filename)
		if err == nil {
			b.fdLoaded = true
			logrus.WithField("fd", b.fd).WithField("name", b.Filename).Info("Loaded map file descriptor.")
		}
		return err
	}
	if !os.IsNotExist(err) {
		return err
	}
	cmd := exec.Command("bpftool", "map", "create", b.Filename,
		"type", b.Type,
		"key", fmt.Sprint(b.KeySize),
		"value", fmt.Sprint(b.ValueSize),
		"entries", fmt.Sprint(b.MaxEntries),
		"name", b.Name,
		"flags", fmt.Sprint(b.Flags),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		logrus.WithField("out", string(out)).Error("Failed to run bpftool")
		return err
	}
	b.fd, err = GetPinnedMapFD(b.Filename)
	if err == nil {
		b.fdLoaded = true
		logrus.WithField("fd", b.fd).WithField("name", b.Filename).Info("Loaded map file descriptor.")
	}
	return err
}
