/*
 * Copyright (C) 2016-2018. ActionTech.
 * Based on: github.com/hashicorp/nomad, github.com/github/gh-ost .
 * License: MPL version 2: https://www.mozilla.org/en-US/MPL/2.0 .
 */

package base

import (
	"fmt"

	uuid "github.com/satori/go.uuid"
	gomysql "github.com/siddontang/go-mysql/mysql"
	"github.com/siddontang/go-mysql/replication"
)

type BinlogEvent struct {
	BinlogFile string
	RealPos    uint32
	Header     *replication.EventHeader
	Evt        replication.Event
	RawBs      []byte
	Query      []string //[]StreamEvent

	Err error
}

// BinlogCoordinates described binary log coordinates in the form of log file & log position.
type BinlogCoordinatesX struct {
	LogFile string
	LogPos  int64
	GtidSet string
}

// String returns a user-friendly string representation of these coordinates
func (b BinlogCoordinatesX) String() string {
	return fmt.Sprintf("%v", b.GtidSet)
}

// IsEmpty returns true if the log file is empty, unnamed
func (b *BinlogCoordinatesX) IsEmpty() bool {
	return b.GtidSet == "" && b.LogFile == ""
}

type GtidItemMap map[uuid.UUID]*GtidItem
type GtidItem struct {
	NRow      int
}
func (m *GtidItemMap) GetItem(u uuid.UUID) (item *GtidItem) {
	item = (*m)[u]
	if item != nil {
		return item
	} else {
		item = &GtidItem{}
		(*m)[u] = item
		return item
	}
}
func GetIntervals(set *gomysql.MysqlGTIDSet, uuidStr string) gomysql.IntervalSlice {
	item, ok := set.Sets[uuidStr]
	if ok {
		return item.Intervals
	} else {
		// Do not modify `set`.
		return nil
	}
}

func IntervalSlicesContainOne(intervals gomysql.IntervalSlice, gno int64) bool {
	for i := range intervals {
		if gno >= intervals[i].Start && gno < intervals[i].Stop {
			return true
		}
	}
	return false
}
