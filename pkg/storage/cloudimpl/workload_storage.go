// Copyright 2019 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package cloudimpl

import (
	"context"
	"io"
	"io/ioutil"
	"net/url"
	"strconv"
	"strings"

	"github.com/cockroachdb/cockroach/pkg/base"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/server/telemetry"
	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
	"github.com/cockroachdb/cockroach/pkg/storage/cloud"
	"github.com/cockroachdb/cockroach/pkg/workload"
	"github.com/cockroachdb/errors"
)

type workloadStorage struct {
	conf     *roachpb.ExternalStorage_Workload
	ioConf   base.ExternalIODirConfig
	gen      workload.Generator
	table    workload.Table
	settings *cluster.Settings
}

var _ cloud.ExternalStorage = &workloadStorage{}

func makeWorkloadStorage(
	ctx context.Context, args ExternalStorageContext, dest roachpb.ExternalStorage,
) (cloud.ExternalStorage, error) {
	telemetry.Count("external-io.workload")
	conf := dest.WorkloadConfig
	if conf == nil {
		return nil, errors.Errorf("workload upload requested but info missing")
	}
	if strings.ToLower(conf.Format) != `csv` {
		return nil, errors.Errorf(`unsupported format: %s`, conf.Format)
	}
	meta, err := workload.Get(conf.Generator)
	if err != nil {
		return nil, err
	}
	// Different versions of the workload could generate different data, so
	// disallow this.
	if meta.Version != conf.Version {
		return nil, errors.Errorf(
			`expected %s version "%s" but got "%s"`, meta.Name, conf.Version, meta.Version)
	}
	gen := meta.New()
	if f, ok := gen.(workload.Flagser); ok {
		if err := f.Flags().Parse(conf.Flags); err != nil {
			return nil, errors.Wrapf(err, `parsing parameters %s`, strings.Join(conf.Flags, ` `))
		}
	}
	s := &workloadStorage{
		conf:     conf,
		ioConf:   args.IOConf,
		gen:      gen,
		settings: args.Settings,
	}
	for _, t := range gen.Tables() {
		if t.Name == conf.Table {
			s.table = t
			break
		}
	}
	if s.table.Name == `` {
		return nil, errors.Errorf(`unknown table %s for generator %s`, conf.Table, meta.Name)
	}
	return s, nil
}

func (s *workloadStorage) Conf() roachpb.ExternalStorage {
	return roachpb.ExternalStorage{
		Provider:       roachpb.ExternalStorageProvider_Workload,
		WorkloadConfig: s.conf,
	}
}

func (s *workloadStorage) ExternalIOConf() base.ExternalIODirConfig {
	return s.ioConf
}

func (s *workloadStorage) Settings() *cluster.Settings {
	return s.settings
}

func (s *workloadStorage) ReadFileAt(
	_ context.Context, basename string, offset int64,
) (io.ReadCloser, int64, error) {
	panic("unimplemented")
}

func (s *workloadStorage) ReadFile(_ context.Context, basename string) (io.ReadCloser, error) {
	if basename != `` {
		return nil, errors.Errorf(`basenames are not supported by workload storage`)
	}
	r := workload.NewCSVRowsReader(s.table, int(s.conf.BatchBegin), int(s.conf.BatchEnd))
	return ioutil.NopCloser(r), nil
}

func (s *workloadStorage) WriteFile(_ context.Context, _ string, _ io.ReadSeeker) error {
	return errors.Errorf(`workload storage does not support writes`)
}

func (s *workloadStorage) ListFiles(_ context.Context, _ string) ([]string, error) {
	return nil, errors.Errorf(`workload storage does not support listing files`)
}

func (s *workloadStorage) Delete(_ context.Context, _ string) error {
	return errors.Errorf(`workload storage does not support deletes`)
}
func (s *workloadStorage) Size(_ context.Context, _ string) (int64, error) {
	return 0, errors.Errorf(`workload storage does not support sizing`)
}
func (s *workloadStorage) Close() error {
	return nil
}

// ParseWorkloadConfig parses a workload config URI to a proto config.
func ParseWorkloadConfig(
	_ ExternalStorageURIContext, uri *url.URL,
) (roachpb.ExternalStorage, error) {
	conf := roachpb.ExternalStorage{}
	conf.Provider = roachpb.ExternalStorageProvider_Workload
	c := &roachpb.ExternalStorage_Workload{}
	pathParts := strings.Split(strings.Trim(uri.Path, `/`), `/`)
	if len(pathParts) != 3 {
		return conf, errors.Errorf(
			`path must be of the form /<format>/<generator>/<table>: %s`, uri.Path)
	}
	c.Format, c.Generator, c.Table = pathParts[0], pathParts[1], pathParts[2]
	q := uri.Query()
	if _, ok := q[`version`]; !ok {
		return conf, errors.New(`parameter version is required`)
	}
	c.Version = q.Get(`version`)
	q.Del(`version`)
	if s := q.Get(`row-start`); len(s) > 0 {
		q.Del(`row-start`)
		var err error
		if c.BatchBegin, err = strconv.ParseInt(s, 10, 64); err != nil {
			return conf, err
		}
	}
	if e := q.Get(`row-end`); len(e) > 0 {
		q.Del(`row-end`)
		var err error
		if c.BatchEnd, err = strconv.ParseInt(e, 10, 64); err != nil {
			return conf, err
		}
	}
	for k, vs := range q {
		for _, v := range vs {
			c.Flags = append(c.Flags, `--`+k+`=`+v)
		}
	}
	conf.WorkloadConfig = c
	return conf, nil
}
