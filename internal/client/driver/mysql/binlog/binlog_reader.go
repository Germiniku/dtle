/*
 * Copyright (C) 2016-2018. ActionTech.
 * Based on: github.com/hashicorp/nomad, github.com/github/gh-ost .
 * License: MPL version 2: https://www.mozilla.org/en-US/MPL/2.0 .
 */

package binlog

import (
	gosql "database/sql"
	"github.com/cznic/mathutil"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/actiontech/dtle/internal/client/driver/common"
	"github.com/pingcap/dm/dm/pb"
	"github.com/pingcap/dm/pkg/gtid"
	"github.com/pingcap/dm/pkg/streamer"
	"github.com/sirupsen/logrus"

	"github.com/actiontech/dtle/internal/g"
	//"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	//"os"

	"github.com/pingcap/parser"
	"github.com/pingcap/parser/ast"
	_ "github.com/pingcap/tidb/types/parser_driver"

	"github.com/satori/go.uuid"
	gomysql "github.com/siddontang/go-mysql/mysql"
	"github.com/siddontang/go-mysql/replication"
	"golang.org/x/net/context"

	"github.com/actiontech/dtle/internal/client/driver/mysql/base"
	"github.com/actiontech/dtle/internal/client/driver/mysql/sql"
	sqle "github.com/actiontech/dtle/internal/client/driver/mysql/sqle/inspector"
	"github.com/actiontech/dtle/internal/client/driver/mysql/util"
	"github.com/actiontech/dtle/internal/config"
	"github.com/actiontech/dtle/internal/config/mysql"

	"github.com/actiontech/dtle/internal/models"
	"github.com/actiontech/dtle/utils"
	"github.com/opentracing/opentracing-go"

	dmrelay "github.com/pingcap/dm/relay"
	"github.com/shirou/gopsutil/mem"
	"runtime/debug"
)

// BinlogReader is a general interface whose implementations can choose their methods of reading
// a binary log file and parsing it into binlog entries
type BinlogReader struct {
	serverId         uint64
	execCtx          *common.ExecContext
	logger           *logrus.Entry
	connectionConfig *mysql.ConnectionConfig
	db               *gosql.DB
	relay            dmrelay.Process
	relayCancelF     context.CancelFunc
	// for direct stream
	binlogSyncer *replication.BinlogSyncer
	// for relay
	binlogStreamer streamer.Streamer
	// for relay
	binlogReader             *streamer.BinlogReader
	currentCoordinates       base.BinlogCoordinateTx
	currentCoordinatesMutex  *sync.Mutex
	LastAppliedRowsEventHint base.BinlogCoordinateTx
	// raw config, whose ReplicateDoDB is same as config file (empty-is-all & no dynamically created tables)
	mysqlContext *config.MySQLDriverConfig
	// dynamic config, include all tables (implicitly assigned or dynamically created)
	tables map[string](map[string]*config.TableContext)

	currentBinlogEntry *BinlogEntry
	ReMap              map[string]*regexp.Regexp

	wg           sync.WaitGroup
	shutdown     bool
	shutdownCh   chan struct{}
	shutdownLock sync.Mutex

	sqlFilter *SqlFilter

	context *sqle.Context
}

type SqlFilter struct {
	NoDML       bool
	NoDMLDelete bool
	NoDMLInsert bool
	NoDMLUpdate bool

	NoDDL            bool
	NoDDLCreateTable bool
	NoDDLDropTable   bool
	NoDDLAlterTable  bool

	NoDDLAlterTableAddColumn    bool
	NoDDLAlterTableDropColumn   bool
	NoDDLAlterTableModifyColumn bool
	NoDDLAlterTableChangeColumn bool
	NoDDLAlterTableAlterColumn  bool
}

func parseSqlFilter(strs []string) (*SqlFilter, error) {
	s := &SqlFilter{}
	for i := range strs {
		switch strings.ToLower(strs[i]) {
		case "nodml":
			s.NoDML = true
		case "nodmldelete":
			s.NoDMLDelete = true
		case "nodmlinsert":
			s.NoDMLInsert = true
		case "nodmlupdate":
			s.NoDMLUpdate = true

		case "noddl":
			s.NoDDL = true
		case "noddlcreatetable":
			s.NoDDLCreateTable = true
		case "noddldroptable":
			s.NoDDLDropTable = true
		case "noddlaltertable":
			s.NoDDLAlterTable = true

		case "noddlaltertableaddcolumn":
			s.NoDDLAlterTableAddColumn = true
		case "noddlaltertabledropcolumn":
			s.NoDDLAlterTableDropColumn = true
		case "noddlaltertablemodifycolumn":
			s.NoDDLAlterTableModifyColumn = true
		case "noddlaltertablechangecolumn":
			s.NoDDLAlterTableChangeColumn = true
		case "noddlaltertablealtercolumn":
			s.NoDDLAlterTableAlterColumn = true

		default:
			return nil, fmt.Errorf("unknown sql filter item: %v", strs[i])
		}
	}
	return s, nil
}

func NewMySQLReader(execCtx *common.ExecContext, cfg *config.MySQLDriverConfig, logger *logrus.Entry, replicateDoDb []*config.DataSource, sqleContext *sqle.Context) (binlogReader *BinlogReader, err error) {
	sqlFilter, err := parseSqlFilter(cfg.SqlFilter)
	if err != nil {
		return nil, err
	}

	binlogReader = &BinlogReader{
		execCtx:                 execCtx,
		logger:                  logger,
		currentCoordinates:      base.BinlogCoordinateTx{},
		currentCoordinatesMutex: &sync.Mutex{},
		mysqlContext:            cfg,
		ReMap:                   make(map[string]*regexp.Regexp),
		shutdownCh:              make(chan struct{}),
		tables:                  make(map[string](map[string]*config.TableContext)),
		sqlFilter:               sqlFilter,
		context:                 sqleContext,
	}

	for _, db := range replicateDoDb {
		tableMap := binlogReader.getDbTableMap(db.TableSchema)
		for _, table := range db.Tables {
			if err := binlogReader.addTableToTableMap(tableMap, table); err != nil {
				return nil, err
			}
		}
	}

	uri := cfg.ConnectionConfig.GetDBUri()
	if binlogReader.db, err = sql.CreateDB(uri); err != nil {
		return nil, err
	}

	id, err := util.NewIdWorker(2, 3, util.SnsEpoch)
	if err != nil {
		return nil, err
	}
	sid, err := id.NextId()
	if err != nil {
		return nil, err
	}
	bid := []byte(strconv.FormatUint(uint64(sid), 10))
	binlogReader.serverId, err = strconv.ParseUint(string(bid), 10, 32)
	if err != nil {
		return nil, err
	}
	logger.Debug("job.start: debug server id is :", binlogReader.serverId)
	// support regex
	binlogReader.genRegexMap()

	if binlogReader.mysqlContext.BinlogRelay {
		// init when connecting
	} else {
		binlogSyncerConfig := replication.BinlogSyncerConfig{
			ServerID:       uint32(binlogReader.serverId),
			Flavor:         "mysql",
			Host:           cfg.ConnectionConfig.Host,
			Port:           uint16(cfg.ConnectionConfig.Port),
			User:           cfg.ConnectionConfig.User,
			Password:       cfg.ConnectionConfig.Password,
			RawModeEnabled: false,
			UseDecimal:     true,

			MaxReconnectAttempts: 10,
			HeartbeatPeriod:      3 * time.Second,
			ReadTimeout:          12 * time.Second,
		}
		binlogReader.binlogSyncer = replication.NewBinlogSyncer(binlogSyncerConfig)
	}

	binlogReader.mysqlContext.Stage = models.StageRegisteringSlaveOnMaster

	return binlogReader, err
}

func (b *BinlogReader) getDbTableMap(schemaName string) map[string]*config.TableContext {
	tableMap, ok := b.tables[schemaName]
	if !ok {
		tableMap = make(map[string]*config.TableContext)
		b.tables[schemaName] = tableMap
	}
	return tableMap
}
func (b *BinlogReader) addTableToTableMap(tableMap map[string]*config.TableContext, table *config.Table) error {
	if table.Where == "" {
		b.logger.Warnf("DTLE_BUG: NewMySQLReader: table.Where is empty (#177 like)")
		table.Where = "true"
	}
	whereCtx, err := config.NewWhereCtx(table.Where, table)
	if err != nil {
		b.logger.Errorf("mysql.reader: Error parse where '%v'", table.Where)
		return err
	}

	tableMap[table.TableName] = config.NewTableContext(table, whereCtx)
	return nil
}

func (b *BinlogReader) getBinlogDir() string {
	return path.Join(b.execCtx.StateDir, "binlog", b.execCtx.Subject)
}

// ConnectBinlogStreamer
func (b *BinlogReader) ConnectBinlogStreamer(coordinates base.BinlogCoordinatesX) (err error) {
	if coordinates.IsEmpty() {
		b.logger.Warnf("mysql.reader: Emptry coordinates at ConnectBinlogStreamer")
	}

	b.currentCoordinates = base.BinlogCoordinateTx{
		LogFile: coordinates.LogFile,
		LogPos:  coordinates.LogPos,
	}
	b.logger.Printf("mysql.reader: Connecting binlog streamer at file %v pos %v gtid %v",
		coordinates.LogFile, coordinates.LogPos, coordinates.GtidSet)

	if b.mysqlContext.BinlogRelay {
		startPos := gomysql.Position{Pos: uint32(coordinates.LogPos), Name: coordinates.LogFile}

		dbConfig := dmrelay.DBConfig{
			Host:     b.mysqlContext.ConnectionConfig.Host,
			Port:     b.mysqlContext.ConnectionConfig.Port,
			User:     b.mysqlContext.ConnectionConfig.User,
			Password: b.mysqlContext.ConnectionConfig.Password,
		}

		// Default to replay position. Change to local relay position if the relay log exists.
		var relayGtid string = coordinates.GtidSet

		relayConfig := &dmrelay.Config{
			ServerID:   int(b.serverId),
			Flavor:     "mysql",
			From:       dbConfig,
			RelayDir:   b.getBinlogDir(),
			BinLogName: "",
			EnableGTID: true,
			BinlogGTID: relayGtid,
		}
		b.relay = dmrelay.NewRelay(relayConfig)
		err = b.relay.Init()
		if err != nil {
			return err
		}

		meta := b.relay.GetMeta()

		{ // Update relayGtid if metaGtid is not empty, aka if there is local relaylog.
			_, metaGtid := meta.GTID()
			metaGtidStr := metaGtid.String()
			if metaGtidStr != "" {
				relayGtid = metaGtidStr
			}
		}

		changed, err := meta.AdjustWithStartPos(coordinates.LogFile, relayGtid, true)
		if err != nil {
			return err
		}
		b.logger.Debugf("*** after AdjustWithStartPos. changed %v", changed)

		ch := make(chan pb.ProcessResult)
		var ctx context.Context
		ctx, b.relayCancelF = context.WithCancel(context.Background())

		go b.relay.Process(ctx, ch)

		loc, err := time.LoadLocation("Local") // TODO

		brConfig := &streamer.BinlogReaderConfig{
			RelayDir: b.getBinlogDir(),
			Timezone: loc,
		}
		b.binlogReader = streamer.NewBinlogReader(brConfig)

		targetGtid, err := gtid.ParserGTID("mysql", coordinates.GtidSet)
		if err != nil {
			return err
		}

		chWait := make(chan struct{})
		go func() {
			waitForRelay := true
			for !b.shutdown {
				_, p := meta.Pos()
				_, gs := meta.GTID()

				if waitForRelay {
					if targetGtid.Contain(gs) {
						b.logger.Debugf("mysql.reader: Relay: keep waiting. pos %v gs %v", p, gs)
					} else {
						b.logger.Debugf("mysql.reader: Relay: stop waiting. pos %v gs %v", p, gs)
						chWait <- struct{}{}
						waitForRelay = false
					}
				}

				time.Sleep(1 * time.Second)
			}
		}()

		<-chWait
		b.binlogStreamer, err = b.binlogReader.StartSync(startPos)
		if err != nil {
			b.logger.Debugf("mysql.reader: err at StartSync: %v", err)
			return err
		}
	} else {
		// Start sync with sepcified binlog gtid
		b.logger.WithField("coordinate", coordinates).Debugf("mysql.reader: will start sync")

		if coordinates.GtidSet == "" {
			b.binlogStreamer, err = b.binlogSyncer.StartSync(
				gomysql.Position{Name: coordinates.LogFile, Pos: uint32(coordinates.LogPos)})
		} else {
			gtidSet, err := gomysql.ParseMysqlGTIDSet(coordinates.GtidSet)
			if err != nil {
				b.logger.Errorf("mysql.reader: err: %v", err)
				return err
			}

			b.binlogStreamer, err = b.binlogSyncer.StartSyncGTID(gtidSet)
		}
		if err != nil {
			b.logger.Debugf("mysql.reader: err at StartSyncGTID: %v", err)
			return err
		}
	}
	b.mysqlContext.Stage = models.StageRequestingBinlogDump

	return nil
}

func (b *BinlogReader) GetCurrentBinlogCoordinates() *base.BinlogCoordinateTx {
	b.currentCoordinatesMutex.Lock()
	defer b.currentCoordinatesMutex.Unlock()
	returnCoordinates := b.currentCoordinates
	return &returnCoordinates
}

func ToColumnValuesV2(abstractValues []interface{}, table *config.TableContext) *mysql.ColumnValues {
	result := &mysql.ColumnValues{
		AbstractValues: make([]*interface{}, len(abstractValues)),
	}

	for i := 0; i < len(abstractValues); i++ {
		if table != nil {
			columns := table.Table.OriginalTableColumns.Columns
			if i < len(columns) && columns[i].IsUnsigned {
				// len(columns) might less than len(abstractValues), esp on AliRDS. See #192.
				switch v := abstractValues[i].(type) {
				case int8:
					abstractValues[i] = uint8(v)
				case int16:
					abstractValues[i] = uint16(v)
				case int32:
					if columns[i].Type == mysql.MediumIntColumnType {
						abstractValues[i] = uint32(v) & 0x00FFFFFF
					} else {
						abstractValues[i] = uint32(v)
					}
				case int64:
					abstractValues[i] = uint64(v)
				}
			}
		}
		result.AbstractValues[i] = &abstractValues[i]
	}

	return result
}

// If isDDL, a sql correspond to a table item, aka len(tables) == len(sqls).
type parseDDLResult struct {
	isDDL  bool
	tables []SchemaTable
	sqls   []string
	ast    ast.StmtNode
}

// StreamEvents
func (b *BinlogReader) handleEvent(ev *replication.BinlogEvent, entriesChannel chan<- *BinlogEntry) error {
	emitBinlogEntry := func() {
		b.logger.WithField("gno", b.currentBinlogEntry.Coordinates.GNO).Debug("emitBinlogEntry")
		entriesChannel <- b.currentBinlogEntry
	}
	spanContext := ev.SpanContest
	trace := opentracing.GlobalTracer()
	span := trace.StartSpan("incremental  binlogEvent translation to  sql", opentracing.ChildOf(spanContext))
	span.SetTag("begin to translation", time.Now().Unix())
	defer span.Finish()
	if b.currentCoordinates.SmallerThanOrEquals(&b.LastAppliedRowsEventHint) {
		b.logger.WithField("lastPos", b.LastAppliedRowsEventHint.LogPos).Debugf(
			"mysql.reader: Skipping handled query at %+v", b.currentCoordinates)
		return nil
	}

	switch ev.Header.EventType {
	case replication.GTID_EVENT:
		evt := ev.Event.(*replication.GTIDEvent)
		b.currentCoordinatesMutex.Lock()
		// TODO this does not unlock until function return. wrap with func() if needed
		defer b.currentCoordinatesMutex.Unlock()
		u, _ := uuid.FromBytes(evt.SID)
		b.currentCoordinates.SID = u
		b.currentCoordinates.GNO = evt.GNO
		b.currentCoordinates.LastCommitted = evt.LastCommitted
		b.currentCoordinates.SeqenceNumber = evt.SequenceNumber
		b.currentBinlogEntry = NewBinlogEntryAt(b.currentCoordinates)
		b.logger.WithField("gno", evt.GNO).Trace("mysql.reader: gtid event")
	case replication.QUERY_EVENT:
		evt := ev.Event.(*replication.QueryEvent)
		query := string(evt.Query)

		if evt.ErrorCode != 0 {
			b.logger.Errorf("DTLE_BUG: found query_event with error code, which is not handled. ec: %v, query: %v",
				evt.ErrorCode, query)
		}

		b.logger.Debugf("mysql.reader: query event: schema: %s, query: %s", evt.Schema, query)

		if strings.ToUpper(query) == "BEGIN" {
			b.currentBinlogEntry.hasBeginQuery = true
		} else {
			if strings.ToUpper(query) == "COMMIT" || !b.currentBinlogEntry.hasBeginQuery {
				currentSchema := string(evt.Schema)
				if b.mysqlContext.SkipCreateDbTable {
					if skipCreateDbTable(query) {
						b.logger.Warnf("mysql.reader: skip create db/table %s", query)
						return nil
					}
				}

				if !b.mysqlContext.ExpandSyntaxSupport {
					if skipQueryEvent(query) {
						b.logger.Warnf("mysql.reader: skip query %s", query)
						return nil
					}
				}

				ddlInfo, err := resolveDDLSQL(query)
				if err != nil || !ddlInfo.isDDL {
					if err != nil {
						b.logger.WithError(err).WithField("query", query).Warn("mysql.reader: failed to parse a query. will execute")
					} else if !ddlInfo.isDDL {
						b.logger.WithField("query", query).Debug("mysql.reader: QueryEvent is not a DDL")
					}
					event := NewQueryEvent(
						currentSchema,
						query,
						NotDML,
						ev.Header.Timestamp,
					)
					b.currentBinlogEntry.Events = append(b.currentBinlogEntry.Events, event)
					b.currentBinlogEntry.SpanContext = span.Context()
					b.currentBinlogEntry.OriginalSize += len(ev.RawData)
					emitBinlogEntry()
					b.LastAppliedRowsEventHint = b.currentCoordinates
					return nil
				}

				skipEvent := false

				b.context.LoadSchemas(nil)
				if currentSchema != "" {
					b.context.UseSchema(currentSchema)
				}
				b.context.UpdateContext(ddlInfo.ast, "mysql")

				if b.sqlFilter.NoDDL {
					skipEvent = true
				}

				b.logger.WithField("len", len(ddlInfo.sqls)).Debug("mysql.reader: DDL parsed and splitted")
				for i, sql := range ddlInfo.sqls {
					realSchema := utils.StringElse(ddlInfo.tables[i].Schema, currentSchema)
					tableName := ddlInfo.tables[i].Table
					err = b.checkObjectFitRegexp(b.mysqlContext.ReplicateDoDb, realSchema, tableName)
					if err != nil {
						b.logger.WithFields(logrus.Fields{
							"gno": b.currentCoordinates.GNO,
							"sql": query,
						}).Warn("mysql.reader: skip query")
						return nil
					}

					if b.skipQueryDDL(sql, realSchema, tableName) {
						b.logger.WithFields(logrus.Fields{
							"gno": b.currentCoordinates.GNO,
							"use": currentSchema,
							"schema": realSchema,
							"table": tableName,
							"sql": sql,
						}).Info("mysql.reader: Skip QueryEvent")
					} else {
						var table *config.Table
						var schema *config.DataSource
						for i := range b.mysqlContext.ReplicateDoDb {
							if b.mysqlContext.ReplicateDoDb[i].TableSchema == realSchema {
								schema = b.mysqlContext.ReplicateDoDb[i]
								for j := range b.mysqlContext.ReplicateDoDb[i].Tables {
									if b.mysqlContext.ReplicateDoDb[i].Tables[j].TableName == tableName {
										table = b.mysqlContext.ReplicateDoDb[i].Tables[j]
									}
								}
							}
						}

						switch realAst := ddlInfo.ast.(type) {
						case *ast.CreateDatabaseStmt:
							b.context.LoadTables(ddlInfo.tables[i].Schema, nil)
						case *ast.CreateTableStmt:
							b.logger.Debugf("mysql.reader: ddl is create table")
							err := b.updateTableMeta(table, realSchema, tableName)
							if err != nil {
								return err
							}

							if b.sqlFilter.NoDDLCreateTable {
								skipEvent = true
							}
						case *ast.DropTableStmt:
							if b.sqlFilter.NoDDLDropTable {
								skipEvent = true
							}
						case *ast.AlterTableStmt:
							b.logger.Debugf("mysql.reader: ddl is alter table. specs: %v", realAst.Specs)

							fromTable := table
							tableNameX := tableName

							for iSpec := range realAst.Specs {
								switch realAst.Specs[iSpec].Tp {
								case ast.AlterTableRenameTable:
									fromTable = nil
									tableNameX = realAst.Specs[iSpec].NewTable.Name.O
								default:
									// do nothing
								}
							}
							err := b.updateTableMeta(fromTable, realSchema, tableNameX)
							if err != nil {
								return err
							}

							if b.sqlFilter.NoDDLAlterTable {
								skipEvent = true
							} else {
								for i := range realAst.Specs {
									switch realAst.Specs[i].Tp {
									case ast.AlterTableAddColumns:
										if b.sqlFilter.NoDDLAlterTableAddColumn {
											skipEvent = true
										}
									case ast.AlterTableDropColumn:
										if b.sqlFilter.NoDDLAlterTableDropColumn {
											skipEvent = true
										}
									case ast.AlterTableModifyColumn:
										if b.sqlFilter.NoDDLAlterTableModifyColumn {
											skipEvent = true
										}
									case ast.AlterTableChangeColumn:
										if b.sqlFilter.NoDDLAlterTableChangeColumn {
											skipEvent = true
										}
									case ast.AlterTableAlterColumn:
										if b.sqlFilter.NoDDLAlterTableAlterColumn {
											skipEvent = true
										}
									default:
										// other case
									}
								}
							}
						}
						if schema != nil && schema.TableSchemaRename != "" {
							ddlInfo.tables[i].Schema = schema.TableSchemaRename
							b.logger.Debugf("mysql.reader. ddl schema mapping :from  %s to %s", realSchema, schema.TableSchemaRename)
							//sql = strings.Replace(sql, realSchema, schema.TableSchemaRename, 1)
							sql = loadMapping(sql, realSchema, schema.TableSchemaRename, "schemaRename", " ")
							if currentSchema == realSchema {
								currentSchema = schema.TableSchemaRename
							}
							realSchema = schema.TableSchemaRename
						} else {
							// schema == nil means it is not explicit in ReplicateDoDb, thus no renaming
							// or schema.TableSchemaRename == "" means no renaming
						}

						if table != nil && table.TableRename != "" {
							ddlInfo.tables[i].Table = table.TableRename
							//sql = strings.Replace(sql, tableName, table.TableRename, 1)
							sql = loadMapping(sql, tableName, table.TableRename, "", currentSchema)
							b.logger.Debugf("mysql.reader. ddl table mapping  :from %s to %s", tableName, table.TableRename)
						}

						if skipEvent {
							b.logger.Debugf("mysql.reader. skipped a ddl event. query: %v", query)
						} else {
							ddlTable := ddlInfo.tables[i]
							if realSchema == "" || ddlTable.Table == "" {
								b.logger.WithFields(logrus.Fields{
									"schema": realSchema,
									"table":  ddlTable.Table,
									"query":  sql,
								}).Info("NewQueryEventAffectTable. found empty schema or table.")
							}
							event := NewQueryEventAffectTable(
								currentSchema,
								sql,
								NotDML,
								ddlTable,
								ev.Header.Timestamp,
							)
							event.Table = table
							b.currentBinlogEntry.Events = append(b.currentBinlogEntry.Events, event)
						}
					}
				}
				b.currentBinlogEntry.SpanContext = span.Context()
				b.currentBinlogEntry.OriginalSize += len(ev.RawData)
				emitBinlogEntry()
				b.LastAppliedRowsEventHint = b.currentCoordinates
			}
		}
	case replication.XID_EVENT:
		b.currentBinlogEntry.SpanContext = span.Context()
		b.currentCoordinates.LogPos = int64(ev.Header.LogPos)
		// TODO is the pos the start or the end of a event?
		// pos if which event should be use? Do we need +1?
		b.currentBinlogEntry.Coordinates.LogPos = b.currentCoordinates.LogPos
		emitBinlogEntry()
		b.LastAppliedRowsEventHint = b.currentCoordinates
	default:
		if rowsEvent, ok := ev.Event.(*replication.RowsEvent); ok {
			dml := ToEventDML(ev.Header.EventType)
			skip, table := b.skipRowEvent(rowsEvent, dml)
			if skip {
				b.logger.Debugf("mysql.reader: skip rowsEvent %s.%s %v", rowsEvent.Table.Schema, rowsEvent.Table.Table, b.currentCoordinates.GNO)
				return nil
			}

			schemaName := string(rowsEvent.Table.Schema)
			tableName := string(rowsEvent.Table.Table)

			if b.sqlFilter.NoDML ||
				(b.sqlFilter.NoDMLDelete && dml == DeleteDML) ||
				(b.sqlFilter.NoDMLInsert && dml == InsertDML) ||
				(b.sqlFilter.NoDMLUpdate && dml == UpdateDML) {

				b.logger.Debugf("mysql.reader. skipped_a_dml_event. type: %v, table: %v.%v", dml, schemaName, tableName)
				return nil
			}

			if dml == NotDML {
				return fmt.Errorf("Unknown DML type: %s", ev.Header.EventType.String())
			}
			dmlEvent := NewDataEvent(
				schemaName,
				tableName,
				dml,
				int(rowsEvent.ColumnCount),
				ev.Header.Timestamp,
			)
			dmlEvent.LogPos = int64(ev.Header.LogPos - ev.Header.EventSize)

			if table != nil && !table.DefChangedSent {
				b.logger.WithField("schema", schemaName).WithField("table", tableName).
					Debug("send table structure")
				dmlEvent.Table = table.Table
				if table.Table == nil {
					b.logger.WithField("schema", schemaName).WithField("table", tableName).
						Warn("DTLE_BUG binlog_reader: table.Table is nil")
				}
				table.DefChangedSent = true
			}

			/*originalTableColumns, _, err := b.InspectTableColumnsAndUniqueKeys(string(rowsEvent.Table.Schema), string(rowsEvent.Table.Table))
			if err != nil {
				return err
			}
			dmlEvent.OriginalTableColumns = originalTableColumns*/

			// It is hard to calculate exact row size. We use estimation.
			avgRowSize := len(ev.RawData) / len(rowsEvent.Rows)

			for i, row := range rowsEvent.Rows {
				b.logger.Tracef("mysql.reader: row values: %v", row[:mathutil.Min(len(row), g.LONG_LOG_LIMIT)])
				if dml == UpdateDML && i%2 == 1 {
					// An update has two rows (WHERE+SET)
					// We do both at the same time
					continue
				}
				switch dml {
				case InsertDML:
					{
						dmlEvent.NewColumnValues = ToColumnValuesV2(row, table)
					}
				case UpdateDML:
					{
						dmlEvent.WhereColumnValues = ToColumnValuesV2(row, table)
						dmlEvent.NewColumnValues = ToColumnValuesV2(rowsEvent.Rows[i+1], table)
					}
				case DeleteDML:
					{
						dmlEvent.WhereColumnValues = ToColumnValuesV2(row, table)
					}
				}

				//b.logger.Debugf("event before row: %v", dmlEvent.WhereColumnValues)
				//b.logger.Debugf("event after row: %v", dmlEvent.NewColumnValues)
				whereTrue := true
				var err error
				if table != nil && !table.WhereCtx.IsDefault {
					switch dml {
					case InsertDML:
						whereTrue, err = table.WhereTrue(dmlEvent.NewColumnValues)
						if err != nil {
							return err
						}
					case UpdateDML:
						before, err := table.WhereTrue(dmlEvent.WhereColumnValues)
						if err != nil {
							return err
						}
						after, err := table.WhereTrue(dmlEvent.NewColumnValues)
						if err != nil {
							return err
						}
						if before != after {
							return fmt.Errorf("update on 'where columns' cause inconsistency")
							// TODO split it to delete + insert to allow such update
						} else {
							whereTrue = before
						}
					case DeleteDML:
						whereTrue, err = table.WhereTrue(dmlEvent.WhereColumnValues)
						if err != nil {
							return err
						}
					}
				}
				if table != nil && table.Table.TableRename != "" {
					dmlEvent.TableName = table.Table.TableRename
					b.logger.Debugf("mysql.reader. dml  table mapping : from %s to %s", dmlEvent.TableName, table.Table.TableRename)
				}
				for _, schema := range b.mysqlContext.ReplicateDoDb {
					if schema.TableSchema != schemaName {
						continue
					}
					if schema.TableSchemaRename == "" {
						continue
					}
					b.logger.Debugf("mysql.reader. dml  schema mapping: from  %s to %s", dmlEvent.DatabaseName, schema.TableSchemaRename)
					dmlEvent.DatabaseName = schema.TableSchemaRename
				}
				if table != nil && !table.DefChangedSent {
					b.logger.Debug("send table structure", "schema", schemaName, "table", tableName)
					dmlEvent.Table = table.Table
					if table.Table == nil {
						b.logger.Warn("DTLE_BUG binlog_reader: table.Table is nil",
							"schema", schemaName, "table", tableName)
					}

					table.DefChangedSent = true
				}

				if whereTrue {
					if dmlEvent.WhereColumnValues != nil {
						b.currentBinlogEntry.OriginalSize += avgRowSize
					}
					if dmlEvent.NewColumnValues != nil {
						b.currentBinlogEntry.OriginalSize += avgRowSize
					}
					// The channel will do the throttling. Whoever is reding from the channel
					// decides whether action is taken sycnhronously (meaning we wait before
					// next iteration) or asynchronously (we keep pushing more events)
					// In reality, reads will be synchronous
					if table != nil && len(table.Table.ColumnMap) > 0 {
						if dmlEvent.NewColumnValues != nil {
							newRow := make([]*interface{}, len(table.Table.ColumnMap))
							for i := range table.Table.ColumnMap {
								idx := table.Table.ColumnMap[i]
								newRow[i] = dmlEvent.NewColumnValues.AbstractValues[idx]
							}
							dmlEvent.NewColumnValues.AbstractValues = newRow
						}

						if dmlEvent.WhereColumnValues != nil {
							newRow := make([]*interface{}, len(table.Table.ColumnMap))
							for i := range table.Table.ColumnMap {
								idx := table.Table.ColumnMap[i]
								newRow[i] = dmlEvent.WhereColumnValues.AbstractValues[idx]
							}
							dmlEvent.WhereColumnValues.AbstractValues = newRow
						}
					}
					b.currentBinlogEntry.Events = append(b.currentBinlogEntry.Events, dmlEvent)
				} else {
					b.logger.Debugf("event has not passed 'where'")
				}
			}
			return nil
		}
	}
	return nil
}

func loadMapping(sql, beforeName, afterName, mappingType, currentSchema string) string {
	sqlType := strings.Split(sql, " ")[1]
	newSql := ""
	if mappingType == "schemaRename" {
		if sqlType == "DATABASE" || sqlType == "SCHEMA" || sqlType == "database" || sqlType == "schema" {
			newSql = strings.Replace(sql, beforeName, afterName, 1)
			return newSql
		} else if sqlType == "TABLE" || sqlType == "table" {
			strings.Contains(sql, "")
			breakStats := strings.Split(sql, beforeName+".")
			if len(breakStats) > 1 {
				breakStats[0] = breakStats[0] + afterName + "."
				for i := 0; i < len(breakStats); i++ {
					newSql += breakStats[i]
				}
				return newSql
			}

			return sql
		}
	} else {
		breakStats := strings.Split(sql, currentSchema+"."+beforeName)
		if len(breakStats) > 1 {
			breakStats[0] = breakStats[0] + currentSchema + "." + afterName
			for i := 0; i < len(breakStats); i++ {
				newSql += breakStats[i]
			}
			return newSql
		} else {
			return strings.Replace(sql, beforeName, afterName, 1)
		}
	}
	return sql
}





// StreamEvents
func (b *BinlogReader) DataStreamEvents(entriesChannel chan<- *BinlogEntry) error {
	ticker1 := time.NewTicker(1 * time.Second)
	memory,_:=mem.VirtualMemory()
	go func(t *time.Ticker) {
		for {
			<-t.C
			memory, _ = mem.VirtualMemory()
		}
	}(ticker1)
	for {
		// Check for shutdown
		if b.shutdown {
			break
		}
		trace := opentracing.GlobalTracer()
		ev, err := b.binlogStreamer.GetEvent(context.Background())
		if err != nil {
			b.logger.Errorf("mysql.reader error GetEvent. err: %v", err)
			return err
		}
		for float64(memory.Available) / float64(memory.Total) < 0.2 {
			b.logger.Warnf("available memory is lower than 20%% (%v/%v), pause binlog parsing 1s for memory",
				memory.Available, memory.Total)
			debug.FreeOSMemory()
			time.Sleep(1 * time.Second)
		}
		spanContext := ev.SpanContest
		span := trace.StartSpan("DataStreamEvents()  get binlogEvent  from mysql-go ", opentracing.FollowsFrom(spanContext))
		span.SetTag("time", time.Now().Unix())
		ev.SpanContest = span.Context()

		if ev.Header.EventType == replication.HEARTBEAT_EVENT {
			continue
		}
		func() {
			b.currentCoordinatesMutex.Lock()
			defer b.currentCoordinatesMutex.Unlock()
			b.currentCoordinates.LogPos = int64(ev.Header.LogPos)
			b.logger.WithFields(logrus.Fields{
				"pos": b.currentCoordinates.LogPos,
				"type": ev.Header.EventType,
			}).Trace("update LogPos")
		}()

		if ev.Header.EventType == replication.ROTATE_EVENT {
			if rotateEvent, ok := ev.Event.(*replication.RotateEvent); ok {
				func() {
					b.currentCoordinatesMutex.Lock()
					defer b.currentCoordinatesMutex.Unlock()
					b.currentCoordinates.LogFile = string(rotateEvent.NextLogName)
				}()
				b.mysqlContext.Stage = models.StageFinishedReadingOneBinlogSwitchingToNextBinlog
				b.logger.Printf("mysql.reader: Rotate to next log name: %s", rotateEvent.NextLogName)
			} else {
				b.logger.Warnf("mysql.reader: fake rotate_event.")
			}
		} else {
			if err := b.handleEvent(ev, entriesChannel); err != nil {
				return err
			}
		}
		span.Finish()
	}

	return nil
}

// resolveDDLSQL resolve to one ddl sql
// example: drop table test.a,test2.b -> drop table test.a; drop table test2.b;
//
// schemaTables is the schema.table that the query has invalidated. For err or non-DDL, it is nil.
// For DDL, it size equals len(sqls).
func resolveDDLSQL(sql string) (result parseDDLResult, err error) {
	stmt, err := parser.New().ParseOneStmt(sql, "", "")
	if err != nil {
		result.sqls = append(result.sqls, sql)
		return result, err
	}
	result.ast = stmt

	_, result.isDDL = stmt.(ast.DDLNode)
	if !result.isDDL {
		result.sqls = append(result.sqls, sql)
		return result, nil
	}

	appendSql := func(sql string, schema string, table string) {
		result.tables = append(result.tables, SchemaTable{Schema: schema, Table: table})
		result.sqls = append(result.sqls, sql)
	}

	switch v := stmt.(type) {
	case *ast.CreateDatabaseStmt:
		appendSql(sql, v.Name, "")
	case *ast.DropDatabaseStmt:
		appendSql(sql, v.Name, "")
	case *ast.AlterDatabaseStmt:
		appendSql(sql, v.Name, "")
	case *ast.CreateIndexStmt:
		appendSql(sql, v.Table.Schema.O, v.Table.Name.O)
	case *ast.DropIndexStmt:
		appendSql(sql, v.Table.Schema.O, v.Table.Name.O)
	case *ast.TruncateTableStmt:
		appendSql(sql, v.Table.Schema.O, v.Table.Name.O)
	case *ast.CreateTableStmt:
		appendSql(sql, v.Table.Schema.O, v.Table.Name.O)
	case *ast.AlterTableStmt:
		appendSql(sql, v.Table.Schema.O, v.Table.Name.O)
	case *ast.DropTableStmt:
		var ex string
		if v.IfExists {
			ex = "if exists"
		}
		for _, t := range v.Tables {
			var db string
			if t.Schema.O != "" {
				db = fmt.Sprintf("%s.", t.Schema.O)
			}
			s := fmt.Sprintf("drop table %s %s`%s`", ex, db, t.Name.O)
			appendSql(s, t.Schema.O, t.Name.O)
		}
	case *ast.CreateUserStmt, *ast.GrantStmt, *ast.DropUserStmt, *ast.AlterUserStmt:
		appendSql(sql, "mysql", "user")
	case *ast.RenameTableStmt:
		appendSql(sql, v.OldTable.Schema.O, v.OldTable.Name.O)
		// TODO handle extra tables in v.TableToTables[1:]
	default:
		return result, fmt.Errorf("unknown DDL type")
	}

	return result, nil
}

func (b *BinlogReader) skipQueryDDL(sql string, schema string, tableName string) bool {
	switch strings.ToLower(schema) {
	case "mysql":
		if b.mysqlContext.ExpandSyntaxSupport {
			return false
		} else {
			return true
		}
	case "sys", "information_schema", "performance_schema", g.DtleSchemaName:
		return true
	default:
		if len(b.mysqlContext.ReplicateDoDb) > 0 {
			return !b.matchTable(b.mysqlContext.ReplicateDoDb, schema, tableName)
		}
		if len(b.mysqlContext.ReplicateIgnoreDb) > 0 {
			return b.matchTable(b.mysqlContext.ReplicateIgnoreDb, schema, tableName)
		}
		return false
	}
}

func skipCreateDbTable(sql string) bool {
	sql = strings.ToLower(sql)

	if strings.HasPrefix(sql, "create database") {
		return true
	}
	if strings.HasPrefix(sql, "create table") {
		return true
	}

	return false
}

func skipQueryEvent(sql string) bool {
	sql = strings.ToLower(sql)

	if strings.HasPrefix(sql, "flush privileges") {
		return true
	}
	if strings.HasPrefix(sql, "alter user") {
		return true
	}
	if strings.HasPrefix(sql, "create user") {
		return true
	}
	if strings.HasPrefix(sql, "create function") {
		return true
	}
	if strings.HasPrefix(sql, "create procedure") {
		return true
	}
	if strings.HasPrefix(sql, "drop user") {
		return true
	}
	if strings.HasPrefix(sql, "delete from mysql.user") {
		return true
	}
	if strings.HasPrefix(sql, "grant") {
		return true
	}
	if strings.HasPrefix(sql, "rename user") {
		return true
	}
	if strings.HasPrefix(sql, "revoke") {
		return true
	}
	if strings.HasPrefix(sql, "set password") {
		return true
	}

	return false
}

func skipMysqlSchemaEvent(tableLower string) bool {
	switch tableLower {
	case "event", "func", "proc", "tables_priv", "columns_priv", "procs_priv", "user":
		return false
	default:
		return true
	}
}

func (b *BinlogReader) skipRowEvent(rowsEvent *replication.RowsEvent, dml EventDML) (bool, *config.TableContext) {
	tableOrigin := string(rowsEvent.Table.Table)
	tableLower := strings.ToLower(tableOrigin)
	switch strings.ToLower(string(rowsEvent.Table.Schema)) {
	case g.DtleSchemaName:
		switch strings.ToLower(string(rowsEvent.Table.Table)) {
		case g.GtidExecutedTableV2, g.GtidExecutedTableV3, g.GtidExecutedTableV3a:
			// cases: 1. delete for compaction; 2. insert for compaction (gtid interval); 3. normal insert for tx (single gtid)
			// We make no special treat for case 2. That tx has only one insert, which should be ignored.
			if dml == InsertDML {
				if len(rowsEvent.Rows) == 1 {
					sidValue := rowsEvent.Rows[0][1]
					sidByte, ok := sidValue.(string)
					if !ok {
						b.logger.Errorf("cycle-prevention: unrecognized gtid_executed table sid type: %T", sidValue)
					} else {
						sid, err := uuid.FromBytes([]byte(sidByte))
						if err != nil {
							b.logger.Errorf("cycle-prevention: cannot convert sid to uuid: %v", err.Error())
						} else {
							b.currentBinlogEntry.Coordinates.OSID = sid.String()
							b.logger.Debugf("mysql.reader: found an osid: %v", b.currentBinlogEntry.Coordinates.OSID)
						}
					}
				}
				// If OSID is target mysql SID, skip applying the binlogEntry.
				// - Plan B: skip sending at applier: unnecessary sending
				// - Plan A: skip sending at extractor: currently extractor does not know target mysql SID
			}
		}
		return true, nil
	case "mysql":
		if b.mysqlContext.ExpandSyntaxSupport {
			return skipMysqlSchemaEvent(tableLower), nil
		} else {
			return true, nil
		}
	case "sys", "information_schema", "performance_schema":
		return true, nil
	default:
		if len(b.tables) > 0 {
			//if table in tartget Table, do this event
			for schemaName, tableMap := range b.tables {
				if b.matchString(schemaName, string(rowsEvent.Table.Schema)) || schemaName == "" {
					if len(tableMap) == 0 {
						return false, nil // TODO not skipping but TableContext
					}
					for tableName, tableCtx := range tableMap {
						if b.matchString(tableName, tableOrigin) {
							return false, tableCtx
						}
					}
				}
			}
			return true, nil
		}
		if len(b.mysqlContext.ReplicateIgnoreDb) > 0 {
			table := strings.ToLower(string(rowsEvent.Table.Table))
			//if table in tartget Table, do this event
			for _, d := range b.mysqlContext.ReplicateIgnoreDb {
				if b.matchString(d.TableSchema, string(rowsEvent.Table.Schema)) || d.TableSchema == "" {
					if len(d.Tables) == 0 {
						return true, nil
					}
					for _, dt := range d.Tables {
						if b.matchString(dt.TableName, table) {
							return true, nil
						}
					}
				}
			}
			return false, nil
		}
	}
	return false, nil
}

func (b *BinlogReader) matchString(pattern string, t string) bool {
	/*if re, ok := b.ReMap[pattern]; ok {
		return re.MatchString(t)
	}*/
	return pattern == t
}

func (b *BinlogReader) matchTable(patternTBS []*config.DataSource, schemaName string, tableName string) bool {
	for _, pdb := range patternTBS {
		if pdb.TableSchemaScope == "schema" && pdb.TableSchema == schemaName {
			return true
		}
		if pdb.TableSchemaScope == "schemas" {
			reg := regexp.MustCompile(pdb.TableSchemaRegex)
			if reg.MatchString(schemaName) {
				return true
			}
		}
		redb, okdb := b.ReMap[pdb.TableSchema]
		for _, ptb := range pdb.Tables {
			retb, oktb := b.ReMap[ptb.TableName]
			if oktb && okdb {
				if redb.MatchString(schemaName) && retb.MatchString(tableName) {
					return true
				}

			}
			if oktb {
				if retb.MatchString(tableName) && schemaName == pdb.TableSchema {
					return true
				}
			}
			if okdb {
				if redb.MatchString(schemaName) && tableName == ptb.TableName {
					return true
				}
			}

			//create database or drop database
			if tableName == "" {
				if schemaName == pdb.TableSchema {
					return true
				}
			}
			if ptb.TableSchema == schemaName && ptb.TableName == tableName {
				return true
			}
			if pdb.TableSchemaScope == "tables" {
				reg := regexp.MustCompile(ptb.TableRegex)
				if reg.MatchString(tableName) && ptb.TableSchema == schemaName {
					return true
				}
			}
		}
	}

	return false
}

func (b *BinlogReader) genRegexMap() {
	for _, db := range b.mysqlContext.ReplicateDoDb {
		if db.TableSchemaScope != "tables" || db.TableSchemaScope != "schemas" {
			continue
		}
		if _, ok := b.ReMap[db.TableSchema]; !ok {
			b.ReMap[db.TableSchema] = regexp.MustCompile(db.TableSchema[1:])
		}

		for _, tb := range db.Tables {
			/*if tb.TableName[0] == '~' {*/
			if _, ok := b.ReMap[tb.TableName]; !ok {
				b.ReMap[tb.TableName] = regexp.MustCompile(tb.TableName[1:])
			}
			/*}*/
			/*if tb.TableSchema[0] == '~' {*/
			if _, ok := b.ReMap[tb.TableSchema]; !ok {
				b.ReMap[tb.TableSchema] = regexp.MustCompile(tb.TableSchema[1:])
			}
			/*	}*/
		}
	}
}

func (b *BinlogReader) Close() error {
	b.logger.Debugf("*** BinlogReader.Close")
	b.shutdownLock.Lock()
	defer b.shutdownLock.Unlock()

	if b.shutdown {
		return nil
	}
	b.shutdown = true
	close(b.shutdownCh)

	b.wg.Wait()
	if err := sql.CloseDB(b.db); err != nil {
		return err
	}
	// Historically there was a:
	if b.mysqlContext.BinlogRelay {
		b.binlogReader.Close()
		b.relayCancelF()
		b.relay.Close()
	} else {
		b.binlogSyncer.Close()
	}
	// here. A new go-mysql version closes the binlog syncer connection independently.
	// I will go against the sacred rules of comments and just leave this here.
	// This is the year 2017. Let's see what year these comments get deleted.
	return nil
}
func (b *BinlogReader) updateTableMeta(table *config.Table, realSchema string, tableName string) error {
	var err error

	columns, err := base.GetTableColumnsSqle(b.context, realSchema, tableName)
	if err != nil {
		b.logger.Warnf("updateTableMeta: cannot get table info after ddl. err: %v, table %v.%v", err.Error(), realSchema, tableName)
		return err
	}
	b.logger.Debugf("binlog_reader. new columns. table: %v.%v, columns: %v",
		realSchema, tableName, columns.String())

	//var table *config.Table

	if table == nil {
		// a new table (it might be in all db copy since it is not ignored).
		table = config.NewTable(realSchema, tableName)
		table.TableType = "BASE TABLE"
		table.Where = "true"
	}
	table.OriginalTableColumns = columns
	table.ColumnMap = config.BuildColumnMapIndex(table.ColumnMapFrom, table.OriginalTableColumns.Ordinals)
	tableMap := b.getDbTableMap(realSchema)
	err = b.addTableToTableMap(tableMap, table)
	if err != nil {
		b.logger.Error("failed to make table context: %v", err)
		return err
	}

	return nil
}

func (b *BinlogReader) checkObjectFitRegexp(patternTBS []*config.DataSource, schemaName string, tableName string) error {

	for _, schema := range patternTBS {
		if schema.TableSchema == schemaName && schema.TableSchemaScope == "schemas" {
			return nil
		}
	}

	for i, pdb := range patternTBS {
		table := &config.Table{}
		schema := &config.DataSource{}
		if pdb.TableSchemaScope == "schemas" && pdb.TableSchema != schemaName {
			reg := regexp.MustCompile(pdb.TableSchemaRegex)
			if reg.MatchString(schemaName) {
				match := reg.FindStringSubmatchIndex(schemaName)
				schema.TableSchemaRegex = pdb.TableSchemaRegex
				schema.TableSchema = schemaName
				schema.TableSchemaScope = "schemas"
				schema.TableSchemaRename = string(reg.ExpandString(nil, pdb.TableSchemaRenameRegex, schemaName, match))
				b.mysqlContext.ReplicateDoDb = append(b.mysqlContext.ReplicateDoDb, schema)
			}
			break
		}
		for _, table := range pdb.Tables {
			if schema.TableSchema == schemaName && schema.TableSchemaScope == "tables" && table.TableName == tableName {
				return nil
			}
		}
		for _, ptb := range pdb.Tables {
			if pdb.TableSchemaScope == "tables" && ptb.TableName != tableName {
				reg := regexp.MustCompile(ptb.TableRegex)
				if reg.MatchString(tableName) {
					match := reg.FindStringSubmatchIndex(tableName)
					table.TableRegex = ptb.TableRegex
					table.TableName = tableName
					table.TableRename = string(reg.ExpandString(nil, ptb.TableRenameRegex, tableName, match))
					b.mysqlContext.ReplicateDoDb[i].Tables = append(b.mysqlContext.ReplicateDoDb[i].Tables, table)
				}
				//b.genRegexMap()
				break
			}
		}
	}
	return nil
}

func (b *BinlogReader) OnApplierRotate(binlogFile string) {
	if !b.mysqlContext.BinlogRelay {
		// do nothing if BinlogRelay is not enabled
		return
	}

	wrappingDir := b.getBinlogDir()
	fs, err := streamer.ReadDir(wrappingDir)
	if err != nil {
		b.logger.WithError(err).WithField("dir", wrappingDir).Errorf("OnApplierRotate. err at reading dir")
		return
	}

	dir := ""

	for i := range fs {
		// https://pingcap.com/docs-cn/v3.0/reference/tools/data-migration/relay-log/
		// <server-uuid>.<subdir-seq-number>
		// currently there should only be .000001, but we loop to the last one.
		subdir := filepath.Join(wrappingDir, fs[i])
		stat, err := os.Stat(subdir)
		if err != nil {
			b.logger.WithError(err).Errorf("OnApplierRotate. err at stat")
			return
		} else {
			if stat.IsDir() {
				dir = subdir
			} else {
				// meta-files like server-uuid.index
			}
		}
	}

	if dir == "" {
		b.logger.Warnf("OnApplierRotate. empty dir")
		return
	}

	realBinlogFile := normalizeBinlogFilename(binlogFile)

	cmp, err := streamer.CollectBinlogFilesCmp(dir, realBinlogFile, streamer.FileCmpLess)
	if err != nil {
		b.logger.WithError(err).Debugf("OnApplierRotate. err at cmp")
	}
	b.logger.WithField("cmp", cmp).Debugf("OnApplierRotate.")

	for i := range cmp {
		f := filepath.Join(dir, cmp[i])
		b.logger.WithField("file", f).Infof("OnApplierRotate will remove")
		err := os.Remove(f)
		if err != nil {
			b.logger.WithField("file", f).WithError(err).Errorf("OnApplierRotate error when removing binlog file")
		}
	}
}

func normalizeBinlogFilename(name string) string {
	// See `posUUIDSuffixSeparator` in pingcap/dm.
	re := regexp.MustCompile("^(.*)\\|.+(\\..*)$")
	sm := re.FindStringSubmatch(name)
	if len(sm) == 3 {
		return sm[1] + sm[2]
	} else {
		return name
	}
}
