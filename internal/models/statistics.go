/*
 * Copyright (C) 2016-2018. ActionTech.
 * Based on: github.com/hashicorp/nomad, github.com/github/gh-ost .
 * License: MPL version 2: https://www.mozilla.org/en-US/MPL/2.0 .
 */

package models

import (
	gonats "github.com/nats-io/go-nats"
)

const (
	StageFinishedReadingOneBinlogSwitchingToNextBinlog = "Finished reading one binlog; switching to next binlog"
	StageMasterHasSentAllBinlogToSlave                 = "Master has sent all binlog to slave; waiting for more updates"
	StageRegisteringSlaveOnMaster                      = "Registering slave on master"
	StageRequestingBinlogDump                          = "Requesting binlog dump"
	StageSearchingRowsForUpdate                        = "Searching rows for update"
	StageSendingBinlogEventToSlave                     = "Sending binlog event to slave"
	StageSendingData                                   = "Sending data"
	StageSlaveHasReadAllRelayLog                       = "Slave has read all relay log; waiting for more updates"
	StageSlaveWaitingForWorkersToProcessQueue          = "Waiting for slave workers to process their queues"
	StageWaitingForGtidToBeCommitted                   = "Waiting for GTID to be committed"
	StageWaitingForMasterToSendEvent                   = "Waiting for master to send event"
)

type TableStats struct {
	InsertCount int64
	UpdateCount int64
	DelCount    int64
}

type DelayCount struct {
	Num  uint64
	Time uint64
}

type ThroughputStat struct {
	Num  uint64
	Time uint64
}

type MsgStat struct {
	InMsgs   uint64
	OutMsgs  uint64
	InBytes  uint64
	OutBytes uint64
}

type BufferStat struct {
	ExtractorTxQueueSize    int
	ApplierTxQueueSize      int
	ApplierGroupTxQueueSize int
	SendByTimeout           int
	SendBySizeFull          int
}

type CurrentCoordinates struct {
	File     string
	Position int64
	GtidSet  string

	RelayMasterLogFile string
	ReadMasterLogPos   int64
	RetrievedGtidSet   string
	ExecutedGtidSet    string
}

type TaskStatistics struct {
	CurrentCoordinates *CurrentCoordinates
	TableStats         *TableStats
	DelayCount         *DelayCount
	ProgressPct        string
	ExecMasterRowCount int64
	ExecMasterTxCount  int64
	ReadMasterRowCount int64
	ReadMasterTxCount  int64
	ETA                string
	Backlog            string
	ThroughputStat     *ThroughputStat
	MsgStat            gonats.Statistics
	BufferStat         BufferStat
	Stage              string
	Timestamp          int64
	DelayTime          int64
}

type AllocStatistics struct {
	Tasks map[string]*TaskStatistics
}
