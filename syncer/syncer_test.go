// Copyright 2016 PingCAP, Inc.
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

package syncer

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/ngaut/log"
	. "github.com/pingcap/check"
	"github.com/pingcap/tidb-enterprise-tools/dm/config"
	"github.com/pingcap/tidb-enterprise-tools/pkg/filter"
	bf "github.com/pingcap/tidb-tools/pkg/binlog-filter"
	cm "github.com/pingcap/tidb-tools/pkg/column-mapping"
	"github.com/pingcap/tidb/ast"
	gmysql "github.com/siddontang/go-mysql/mysql"
	"github.com/siddontang/go-mysql/replication"
	"golang.org/x/net/context"
)

var _ = Suite(&testSyncerSuite{})

func TestSuite(t *testing.T) {
	TestingT(t)
}

type testSyncerSuite struct {
	db       *sql.DB
	syncer   *replication.BinlogSyncer
	streamer *replication.BinlogStreamer
	cfg      *config.SubTaskConfig
}

func (s *testSyncerSuite) SetUpSuite(c *C) {
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port, _ := strconv.Atoi(os.Getenv("MYSQL_PORT"))
	if port == 0 {
		port = 3306
	}
	user := os.Getenv("MYSQL_USER")
	if user == "" {
		user = "root"
	}
	pswd := os.Getenv("MYSQL_PSWD")

	s.cfg = &config.SubTaskConfig{
		From: config.DBConfig{
			Host:     host,
			User:     user,
			Password: pswd,
			Port:     port,
		},
		To: config.DBConfig{
			Host:     host,
			User:     user,
			Password: pswd,
			Port:     port,
		},
		ServerID: 101,
	}

	var err error
	dbAddr := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8", s.cfg.From.User, s.cfg.From.Password, s.cfg.From.Host, s.cfg.From.Port)
	s.db, err = sql.Open("mysql", dbAddr)
	if err != nil {
		log.Fatal(err)
	}

	s.syncer = replication.NewBinlogSyncer(replication.BinlogSyncerConfig{
		ServerID:       uint32(s.cfg.ServerID),
		Flavor:         "mysql",
		Host:           s.cfg.From.Host,
		Port:           uint16(s.cfg.From.Port),
		User:           s.cfg.From.User,
		Password:       s.cfg.From.Password,
		UseDecimal:     true,
		VerifyChecksum: true,
	})
	s.resetMaster()
	s.streamer, err = s.syncer.StartSync(gmysql.Position{Name: "", Pos: 4})
	if err != nil {
		log.Fatal(err)
	}

	_, err = s.db.Exec("SET GLOBAL binlog_format = 'ROW';")
	c.Assert(err, IsNil)
}

func (s *testSyncerSuite) TearDownSuite(c *C) {
	s.db.Close()
}

func (s *testSyncerSuite) resetMaster() {
	s.db.Exec("reset master")
}

func (s *testSyncerSuite) TestSelectDB(c *C) {
	s.cfg.BWList = &filter.Rules{
		DoDBs: []string{"~^b.*", "s1", "stest"},
	}
	sqls := []string{
		"create database s1",
		"drop database s1",
		"create database s2",
		"drop database s2",
		"create database btest",
		"drop database btest",
		"create database b1",
		"drop database b1",
		"create database stest",
		"drop database stest",
		"create database st",
		"drop database st",
	}
	res := []bool{false, false, true, true, false, false, false, false, false, false, true, true}

	for _, sql := range sqls {
		s.db.Exec(sql)
	}

	p, err := getParser(s.db, false)
	c.Assert(err, IsNil)

	syncer := NewSyncer(s.cfg)
	syncer.genRouter()
	var i int
	for {
		if i == len(sqls) {
			break
		}
		e, err := s.streamer.GetEvent(context.Background())
		c.Assert(err, IsNil)
		ev, ok := e.Event.(*replication.QueryEvent)
		if !ok {
			continue
		}

		sql := string(ev.Query)
		stmt, err := p.ParseOneStmt(sql, "", "")
		c.Assert(err, IsNil)

		tableNames, err := fetchDDLTableNames(string(ev.Schema), stmt)
		c.Assert(err, IsNil)

		r, err := syncer.skipQuery(tableNames, stmt, sql)
		c.Assert(err, IsNil)
		c.Assert(r, Equals, res[i])
		i++
	}
}

func (s *testSyncerSuite) TestSelectTable(c *C) {
	s.cfg.BWList = &filter.Rules{
		DoDBs: []string{"t2", "stest", "~^ptest*"},
		DoTables: []*filter.Table{
			{Schema: "stest", Name: "log"},
			{Schema: "stest", Name: "~^t.*"},
			{Schema: "~^ptest*", Name: "~^t.*"},
		},
	}

	sqls := []string{
		"create database s1",
		"create table s1.log(id int)",
		"drop database s1",

		"create table mysql.test(id int)",
		"drop table mysql.test",
		"create database stest",
		"create table stest.log(id int)",
		"create table stest.t(id int)",
		"create table stest.log2(id int)",
		"insert into stest.t(id) values (10)",
		"insert into stest.log(id) values (10)",
		"insert into stest.log2(id) values (10)",
		"drop table stest.log,stest.t,stest.log2",
		"drop database stest",

		"create database t2",
		"create table t2.log(id int)",
		"create table t2.log1(id int)",
		"drop table t2.log",
		"drop database t2",
		"create database ptest1",
		"create table ptest1.t1(id int)",
		"drop database ptest1",
	}
	res := [][]bool{
		{true},
		{true},
		{true},

		{true},
		{true},
		{false},
		{false},
		{false},
		{true},
		{false},
		{false},
		{true},
		{false, false, true},
		{false},

		{false},
		{true},
		{true},
		{true},
		{false},
		{false},
		{false},
		{false},
	}

	for _, sql := range sqls {
		s.db.Exec(sql)
	}

	p, err := getParser(s.db, false)
	c.Assert(err, IsNil)

	syncer := NewSyncer(s.cfg)
	syncer.genRouter()
	var i int
	for {
		if i == len(sqls) {
			break
		}
		e, err := s.streamer.GetEvent(context.Background())
		c.Assert(err, IsNil)
		switch ev := e.Event.(type) {
		case *replication.QueryEvent:
			query := string(ev.Query)
			querys, err := resolveDDLSQL(query, p)
			c.Assert(err, IsNil)
			if len(querys) == 0 {
				continue
			}

			for j, sql := range querys {
				stmt, err := p.ParseOneStmt(sql, "", "")
				c.Assert(err, IsNil)

				tableNames, err := fetchDDLTableNames(string(ev.Schema), stmt)
				c.Assert(err, IsNil)
				r, err := syncer.skipQuery(tableNames, stmt, sql)
				c.Assert(err, IsNil)
				c.Assert(r, Equals, res[i][j])
			}
		case *replication.RowsEvent:
			r, err := syncer.skipDMLEvent(string(ev.Table.Schema), string(ev.Table.Table), e.Header.EventType)
			c.Assert(err, IsNil)
			c.Assert(r, Equals, res[i][0])
		default:
			continue
		}
		i++
	}
}

func (s *testSyncerSuite) TestIgnoreDB(c *C) {
	s.cfg.BWList = &filter.Rules{
		IgnoreDBs: []string{"~^b.*", "s1", "stest"},
	}

	sqls := []string{
		"create database s1",
		"drop database s1",
		"create database s2",
		"drop database s2",
		"create database btest",
		"drop database btest",
		"create database b1",
		"drop database b1",
		"create database stest",
		"drop database stest",
		"create database st",
		"drop database st",
	}
	res := []bool{true, true, false, false, true, true, true, true, true, true, false, false}

	for _, sql := range sqls {
		s.db.Exec(sql)
	}

	p, err := getParser(s.db, false)
	c.Assert(err, IsNil)

	syncer := NewSyncer(s.cfg)
	syncer.genRouter()
	i := 0
	for {
		if i == len(sqls) {
			break
		}

		e, err := s.streamer.GetEvent(context.Background())
		c.Assert(err, IsNil)
		ev, ok := e.Event.(*replication.QueryEvent)
		if !ok {
			continue
		}

		sql := string(ev.Query)
		stmt, err := p.ParseOneStmt(sql, "", "")
		c.Assert(err, IsNil)

		tableNames, err := fetchDDLTableNames(sql, stmt)
		c.Assert(err, IsNil)
		r, err := syncer.skipQuery(tableNames, stmt, sql)
		c.Assert(err, IsNil)
		c.Assert(r, Equals, res[i])
		i++
	}
}

func (s *testSyncerSuite) TestIgnoreTable(c *C) {
	s.cfg.BWList = &filter.Rules{
		IgnoreDBs: []string{"t2"},
		IgnoreTables: []*filter.Table{
			{Schema: "stest", Name: "log"},
			{Schema: "stest", Name: "~^t.*"},
		},
	}

	sqls := []string{
		"create database s1",
		"create table s1.log(id int)",
		"drop database s1",

		"create table mysql.test(id int)",
		"drop table mysql.test",
		"create database stest",
		"create table stest.log(id int)",
		"create table stest.t(id int)",
		"create table stest.log2(id int)",
		"insert into stest.t(id) values (10)",
		"insert into stest.log(id) values (10)",
		"insert into stest.log2(id) values (10)",
		"drop table stest.log,stest.t,stest.log2",
		"drop database stest",

		"create database t2",
		"create table t2.log(id int)",
		"create table t2.log1(id int)",
		"drop table t2.log",
		"drop database t2",
	}
	res := [][]bool{
		{false},
		{false},
		{false},

		{true},
		{true},
		{false},
		{true},
		{true},
		{false},
		{true},
		{true},
		{false},
		{true, true, false},
		{false},

		{true},
		{true},
		{true},
		{true},
		{true},
	}

	for _, sql := range sqls {
		s.db.Exec(sql)
	}

	p, err := getParser(s.db, false)
	c.Assert(err, IsNil)

	syncer := NewSyncer(s.cfg)
	syncer.genRouter()
	i := 0
	for {
		if i == len(sqls) {
			break
		}
		e, err := s.streamer.GetEvent(context.Background())
		c.Assert(err, IsNil)
		switch ev := e.Event.(type) {
		case *replication.QueryEvent:
			query := string(ev.Query)
			querys, err := resolveDDLSQL(query, p)
			c.Assert(err, IsNil)
			if len(querys) == 0 {
				continue
			}

			for j, sql := range querys {
				stmt, err := p.ParseOneStmt(sql, "", "")
				c.Assert(err, IsNil)

				tableNames, err := fetchDDLTableNames(string(ev.Schema), stmt)
				c.Assert(err, IsNil)
				r, err := syncer.skipQuery(tableNames, stmt, sql)
				c.Assert(err, IsNil)
				c.Assert(r, Equals, res[i][j])
			}
		case *replication.RowsEvent:
			r, err := syncer.skipDMLEvent(string(ev.Table.Schema), string(ev.Table.Table), e.Header.EventType)
			c.Assert(err, IsNil)
			c.Assert(r, Equals, res[i][0])

		default:
			continue
		}

		i++
	}

}

func (s *testSyncerSuite) TestSkipDML(c *C) {
	s.cfg.FilterRules = []*bf.BinlogEventRule{
		{
			SchemaPattern: "*",
			TablePattern:  "",
			Events:        []bf.EventType{bf.UpdateEvent},
			Action:        bf.Ignore,
		}, {
			SchemaPattern: "foo",
			TablePattern:  "",
			Events:        []bf.EventType{bf.DeleteEvent},
			Action:        bf.Ignore,
		}, {
			SchemaPattern: "foo1",
			TablePattern:  "bar1",
			Events:        []bf.EventType{bf.DeleteEvent},
			Action:        bf.Ignore,
		},
	}
	s.cfg.BWList = nil

	sqls := []struct {
		sql     string
		isDML   bool
		skipped bool
	}{
		{"drop database if exists foo", false, false},
		{"create database foo", false, false},
		{"create table foo.bar(id int)", false, false},
		{"insert into foo.bar values(1)", true, false},
		{"update foo.bar set id=2", true, true},
		{"delete from foo.bar where id=2", true, true},
		{"drop database if exists foo1", false, false},
		{"create database foo1", false, false},
		{"create table foo1.bar1(id int)", false, false},
		{"insert into foo1.bar1 values(1)", true, false},
		{"update foo1.bar1 set id=2", true, true},
		{"delete from foo1.bar1 where id=2", true, true},
	}

	for i := range sqls {
		s.db.Exec(sqls[i].sql)
	}

	p, err := getParser(s.db, false)
	c.Assert(err, IsNil)

	syncer := NewSyncer(s.cfg)
	syncer.genRouter()

	syncer.binlogFilter, err = bf.NewBinlogEvent(false, s.cfg.FilterRules)
	c.Assert(err, IsNil)

	i := 0
	for {
		if i >= len(sqls) {
			break
		}
		e, err := s.streamer.GetEvent(context.Background())
		c.Assert(err, IsNil)
		switch ev := e.Event.(type) {
		case *replication.QueryEvent:
			stmt, err := p.ParseOneStmt(string(ev.Query), "", "")
			c.Assert(err, IsNil)
			_, isDDL := stmt.(ast.DDLNode)
			if !isDDL {
				continue
			}

		case *replication.RowsEvent:
			r, err := syncer.skipDMLEvent(string(ev.Table.Schema), string(ev.Table.Table), e.Header.EventType)
			c.Assert(err, IsNil)
			c.Assert(r, Equals, sqls[i].skipped)
		default:
			continue
		}
		i++
	}
}

func (s *testSyncerSuite) TestColumnMapping(c *C) {
	rules := []*cm.Rule{
		{
			PatternSchema: "stest*",
			PatternTable:  "log*",
			TargetColumn:  "id",
			Expression:    cm.AddPrefix,
			Arguments:     []string{"test:"},
		},
		{
			PatternSchema: "stest*",
			PatternTable:  "t*",
			TargetColumn:  "id",
			Expression:    cm.PartitionID,
			Arguments:     []string{"1", "stest_", "t_"},
		},
	}

	createTableSQLs := []string{
		"create database if not exists stest_3",
		"create table if not exists stest_3.log(id varchar(45))",
		"create table if not exists stest_3.t_2(name varchar(45), id bigint)",
		"create table if not exists stest_3.a(id int)",
	}

	dmls := []struct {
		sql    string
		column []string
		data   []interface{}
	}{
		{"insert into stest_3.t_2(name, id) values (\"ian\", 10)", []string{"name", "id"}, []interface{}{"ian", int64(1<<59 | 3<<52 | 2<<44 | 10)}},
		{"insert into stest_3.log(id) values (\"10\")", []string{"id"}, []interface{}{"test:10"}},
		{"insert into stest_3.a(id) values (10)", []string{"id"}, []interface{}{int32(10)}},
	}

	dropTableSQLs := []string{
		"drop table stest_3.log,stest_3.t_2,stest_3.a",
		"drop database stest_3",
	}

	for _, sql := range createTableSQLs {
		s.db.Exec(sql)
	}

	for i := range dmls {
		s.db.Exec(dmls[i].sql)
	}

	for _, sql := range dropTableSQLs {
		s.db.Exec(sql)
	}

	p, err := getParser(s.db, false)
	c.Assert(err, IsNil)

	mapping, err := cm.NewMapping(false, rules)
	c.Assert(err, IsNil)

	totalEvent := len(dmls) + len(createTableSQLs) + len(dropTableSQLs)
	i := 0
	dmlIndex := 0
	for {
		if i == totalEvent {
			break
		}
		e, err := s.streamer.GetEvent(context.Background())
		c.Assert(err, IsNil)
		switch ev := e.Event.(type) {
		case *replication.QueryEvent:
			stmt, err := p.ParseOneStmt(string(ev.Query), "", "")
			c.Assert(err, IsNil)
			_, isDDL := stmt.(ast.DDLNode)
			if !isDDL {
				continue
			}
		case *replication.RowsEvent:
			r, _, err := mapping.HandleRowValue(string(ev.Table.Schema), string(ev.Table.Table), dmls[dmlIndex].column, ev.Rows[0])
			c.Assert(err, IsNil)
			c.Assert(r, DeepEquals, dmls[dmlIndex].data)
			dmlIndex++
		default:
			continue
		}
		i++
	}
}
