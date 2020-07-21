package config

import (
	"errors"
	"fmt"

	"github.com/ghodss/yaml"
	"github.com/hashicorp/go-multierror"
	"github.com/segmentio/kafka-go"
	"github.com/segmentio/topicctl/pkg/admin"
	log "github.com/sirupsen/logrus"
)

type PlacementStrategy string

const (
	// PlacementStrategyAny allows any partition placement.
	PlacementStrategyAny PlacementStrategy = "any"

	// PlacementStrategyBalancedLeaders is a strategy that ensures the leaders of
	// each partition are balanced by rack, but does not care about the placements
	// of the non-leader replicas.
	PlacementStrategyBalancedLeaders PlacementStrategy = "balanced-leaders"

	// PlacementStrategyInRack is a strategy in which the leaders are balanced
	// and the replicas for each partition are in the same rack as the leader.
	PlacementStrategyInRack PlacementStrategy = "in-rack"

	// PlacementStrategyStatic uses a static placement defined in the config. This is for
	// testing only and should generally not be used in production.
	PlacementStrategyStatic PlacementStrategy = "static"

	// PlacementStrategyStaticInRack is a strategy in which the replicas in each partition
	// are chosen from the rack in a static list, but the specific replicas within each partition
	// aren't specified.
	PlacementStrategyStaticInRack PlacementStrategy = "static-in-rack"
)

var allPlacementStrategies = []PlacementStrategy{
	PlacementStrategyAny,
	PlacementStrategyBalancedLeaders,
	PlacementStrategyInRack,
	PlacementStrategyStatic,
	PlacementStrategyStaticInRack,
}

type PickerMethod string

const (
	// PickerMethodClusterUser uses broker frequency in the topic, breaking ties by
	// looking at the total number of replicas across the entire cluster that each broker
	// appears in.
	PickerMethodClusterUse PickerMethod = "cluster-use"

	// PickerMethodLowestIndex uses broker frequency in the topic, breaking ties by
	// choosing the broker with the lowest index.
	PickerMethodLowestIndex PickerMethod = "lowest-index"

	// PickerMethodRandomized uses broker frequency in the topic, breaking ties by
	// using a repeatably random choice from the options.
	PickerMethodRandomized PickerMethod = "randomized"
)

var allPickerMethods = []PickerMethod{
	PickerMethodClusterUse,
	PickerMethodLowestIndex,
	PickerMethodRandomized,
}

// TopicConfig represents the desired configuration of a topic.
type TopicConfig struct {
	Meta TopicMeta `json:"meta"`
	Spec TopicSpec `json:"spec"`
}

// TopicMeta stores the (mostly immutable) metadata associated with a topic.
// Inspired by the meta structs in Kubernetes objects.
type TopicMeta struct {
	Name        string `json:"name"`
	Cluster     string `json:"cluster"`
	Region      string `json:"region"`
	Environment string `json:"environment"`
	Description string `json:"description"`

	// Consumers is a list of consumers who are expected to consume from this
	// topic.
	Consumers []string `json:"consumers,omitempty"`
}

// TopicSpec stores the (mutable) specification for a topic.
type TopicSpec struct {
	Partitions        int `json:"partitions"`
	ReplicationFactor int `json:"replicationFactor"`
	RetentionMinutes  int `json:"retentionMinutes,omitempty"`

	PlacementConfig TopicPlacementConfig  `json:"placement"`
	MigrationConfig *TopicMigrationConfig `json:"migration,omitempty"`

	// TODO: Add compression scheme?
	// TODO: Add support for compaction?
}

// TopicPlacementConfig describes how the partition replicas in a topic
// should be chosen.
type TopicPlacementConfig struct {
	Strategy PlacementStrategy `json:"strategy"`
	Picker   PickerMethod      `json:"picker,omitempty"`

	// StaticAssignments is a list of lists of desired replica assignments. It's used
	// for the "static" strategy only.
	StaticAssignments [][]int `json:"staticAssignments,omitempty"`

	// StaticRackAssignments is a list of list of desired replica assignments. It's used
	// for the "static-in-rack" strategy only.
	StaticRackAssignments []string `json:"staticRackAssignments,omitempty"`
}

// TopicMigrationConfig configures the throttles and batch sizes used when
// running a partition migration. If these are left unset, resonable defaults
// will be used instead.
type TopicMigrationConfig struct {
	ThrottleBytes      int64 `json:"throttleBytes"`
	PartitionBatchSize int   `json:"partitionBatchSize"`
}

// ToNewTopicConfig converts a TopicConfig to a kafka.TopicConfig that can be
// used by kafka-go to create a new topic.
func (t TopicConfig) ToNewTopicConfig() kafka.TopicConfig {
	config := kafka.TopicConfig{
		Topic:             t.Meta.Name,
		NumPartitions:     t.Spec.Partitions,
		ReplicationFactor: t.Spec.ReplicationFactor,
	}

	if t.Spec.RetentionMinutes > 0 {
		config.ConfigEntries = []kafka.ConfigEntry{
			{
				ConfigName:  admin.RetentionKey,
				ConfigValue: fmt.Sprintf("%d", t.Spec.RetentionMinutes*60*1000),
			},
		}
	}

	return config
}

// SetDefaults sets the default migration and placement settings in a topic config
// if these aren't set.
func (t *TopicConfig) SetDefaults() {
	if t.Spec.MigrationConfig == nil {
		t.Spec.MigrationConfig = &TopicMigrationConfig{}
	}

	if t.Spec.MigrationConfig.ThrottleBytes == 0 {
		// Default to 120MB/s
		t.Spec.MigrationConfig.ThrottleBytes = 240000000
	}
	if t.Spec.MigrationConfig.PartitionBatchSize == 0 {
		// Migration partitions one at a time
		t.Spec.MigrationConfig.PartitionBatchSize = 1
	}

	if t.Spec.PlacementConfig.Picker == "" {
		t.Spec.PlacementConfig.Picker = PickerMethodRandomized
	}
}

// Validate evaluates whether the topic config is valid.
func (t TopicConfig) Validate(numRacks int) error {
	var err error

	if t.Meta.Name == "" {
		err = multierror.Append(err, errors.New("Name must be set"))
	}
	if t.Meta.Cluster == "" {
		err = multierror.Append(err, errors.New("Cluster must be set"))
	}
	if t.Meta.Region == "" {
		err = multierror.Append(err, errors.New("Region must be set"))
	}
	if t.Meta.Environment == "" {
		err = multierror.Append(err, errors.New("Environment must be set"))
	}
	if t.Spec.Partitions <= 0 {
		err = multierror.Append(err, errors.New("Partitions must be a positive number"))
	}
	if t.Spec.ReplicationFactor <= 0 {
		err = multierror.Append(err, errors.New("ReplicationFactor must be > 0"))
	}

	placement := t.Spec.PlacementConfig

	strategyIndex := -1
	for s, strategy := range allPlacementStrategies {
		if strategy == placement.Strategy {
			strategyIndex = s
			break
		}
	}

	if strategyIndex == -1 {
		err = multierror.Append(
			err,
			fmt.Errorf(
				"PlacementStrategy must in %+v",
				allPlacementStrategies,
			),
		)
	}

	pickerIndex := -1
	for p, pickerMethod := range allPickerMethods {
		if pickerMethod == placement.Picker {
			pickerIndex = p
			break
		}
	}

	if pickerIndex == -1 {
		err = multierror.Append(
			err,
			fmt.Errorf(
				"PickerMethod must in %+v",
				allPickerMethods,
			),
		)
	}

	switch placement.Strategy {
	case PlacementStrategyBalancedLeaders:
		if numRacks > 0 && t.Spec.Partitions%numRacks != 0 {
			// The balanced-leaders strategy requires that the
			// partitions be a multiple of the number of racks, otherwise it's impossible
			// to find a placement that satisfies the strategy.
			err = multierror.Append(
				err,
				fmt.Errorf(
					"Number of partitions (%d) is not a multiple of the number of racks (%d)",
					t.Spec.Partitions,
					numRacks,
				),
			)
		}
	case PlacementStrategyInRack:
	case PlacementStrategyStatic:
		if len(placement.StaticAssignments) != t.Spec.Partitions {
			err = multierror.Append(
				err,
				errors.New("Static assignments must be same length as partitions"),
			)
		} else {
			for _, replicas := range placement.StaticAssignments {
				if len(replicas) != t.Spec.ReplicationFactor {
					err = multierror.Append(
						err,
						errors.New("Static assignment rows must match replication factor"),
					)
					break
				}
			}
		}
	case PlacementStrategyStaticInRack:
		if len(placement.StaticRackAssignments) != t.Spec.Partitions {
			err = multierror.Append(
				err,
				errors.New("Static rack assignments must be same length as partitions"),
			)
		}
	}

	// Warn about the partition count in the non-balanced-leaders case
	if numRacks > 0 &&
		placement.Strategy != PlacementStrategyBalancedLeaders &&
		t.Spec.Partitions%numRacks != 0 {
		log.Warnf("Number of partitions (%d) is not a multiple of the number of racks (%d)",
			t.Spec.Partitions,
			numRacks,
		)
	}

	return err
}

func (t TopicConfig) ToYAML() (string, error) {
	outBytes, err := yaml.Marshal(t)
	if err != nil {
		return "", err
	}
	return string(outBytes), nil
}
