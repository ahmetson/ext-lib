package handler

import (
	"github.com/charmbracelet/log"

	"github.com/blocklords/gosds/categorizer/event"
	"github.com/blocklords/gosds/db"
	"github.com/blocklords/gosds/static/configuration"
	"github.com/blocklords/gosds/static/smartcontract"

	"github.com/blocklords/gosds/app/remote/message"
	"github.com/blocklords/gosds/common/data_type"
	"github.com/blocklords/gosds/common/data_type/key_value"
	"github.com/blocklords/gosds/common/topic"
)

const SNAPSHOT_LIMIT = uint64(500)

// Return the categorized logs of the SNAPSHOT_LIMIT amount since the block_timestamp_from
// For the topic_filter
//
// This function is called by the Gateway
func GetSnapshot(db_con *db.Database, request message.Request, logger log.Logger) message.Reply {
	/////////////////////////////////////////////////////////////////////////////
	//
	// Extract the parameters
	//
	/////////////////////////////////////////////////////////////////////////////
	block_timestamp_from, err := request.Parameters.GetUint64("block_timestamp_from")
	if err != nil {
		return message.Fail(err.Error())
	}
	topic_filter_map, err := request.Parameters.GetKeyValue("topic_filter")
	if err != nil {
		return message.Fail(err.Error())
	}
	topic_filter := topic.ParseJSONToTopicFilter(topic_filter_map)

	query, parameters := configuration.QueryFilterSmartcontract(topic_filter)

	smartcontracts, _, err := smartcontract.GetFromDatabaseFilterBy(db_con, query, parameters)
	if err != nil {
		return message.Fail("failed to filter smartcontracts by the topic filter:" + err.Error())
	} else if len(smartcontracts) == 0 {
		return message.Fail("no matching smartcontracts for the topic filter " + topic_filter.ToString())
	}

	logs, err := event.GetLogsFromDb(db_con, smartcontracts, block_timestamp_from, SNAPSHOT_LIMIT)
	if err != nil {
		return message.Fail("database error to filter logs: " + err.Error())
	}

	block_timestamp_to := block_timestamp_from
	for _, log := range logs {
		if log.BlockTimestamp > block_timestamp_to {
			block_timestamp_to = log.BlockTimestamp
		}
	}

	reply := message.Reply{
		Status: "OK",
		Parameters: key_value.New(map[string]interface{}{
			"logs":            data_type.ToMapList(logs),
			"block_timestamp": block_timestamp_to,
		}),
	}

	return reply
}
