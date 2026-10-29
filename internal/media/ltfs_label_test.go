package media

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLTFSLabelsPhysicalMapping(t *testing.T) {
	// The same format admits arbitrary symbols, swapped roles and either physical partition order.
	for _, test := range []struct {
		name, physical0, physical1, index, data string
	}{
		{"usual", "a", "b", "a", "b"},
		{"reversed physical order", "b", "a", "a", "b"},
		{"reversed roles", "a", "b", "b", "a"},
		{"arbitrary symbols", "z", "m", "m", "z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts, err := ParseLTFSLabels([2][]byte{
				[]byte(ltfsLabelFixture(test.physical0, test.index, test.data)),
				[]byte(ltfsLabelFixture(test.physical1, test.index, test.data)),
			})
			require.NoError(t, err)
			require.Equal(t, &LTFSLabelFacts{
				VolumeUUID: "30a91a08-daae-48d1-ae75-69804e61d2ea", BlockBytes: 524288, Compression: false,
				IndexPartition: test.index, DataPartition: test.data,
				Partitions: map[string]uint32{test.physical0: 0, test.physical1: 1},
			}, facts)
		})
	}
}

func TestParseLTFSLabelsReferenceCompatibility(t *testing.T) {
	// The studied reference reader accepts Label versions 1.0.0 through 2.99.99 with this shared shape.
	for _, version := range []string{"1.0.0", "2.0.0", "2.4.0", "2.99.99"} {
		t.Run(version, func(t *testing.T) {
			labels := [2][]byte{}
			for physical, partition := range []string{"a", "b"} {
				text := strings.Replace(ltfsLabelFixture(partition, "a", "b"), "2.4.0", version, 1)
				labels[physical] = []byte(text)
			}
			facts, err := ParseLTFSLabels(labels)
			require.NoError(t, err)
			require.Equal(t, map[string]uint32{"a": 0, "b": 1}, facts.Partitions)
		})
	}

	// XML whitespace may fill the record after the document; unknown child tags supply no known facts.
	first := "\xef\xbb\xbf" + strings.Replace(ltfsLabelFixture("a", "a", "b"), "</ltfslabel>",
		"<extension><volumeuuid>not-the-volume</volumeuuid></extension></ltfslabel>\r\n\t", 1)
	second := ltfsLabelFixture("b", "a", "b")
	labels := [2][]byte{
		append([]byte(first), bytes.Repeat([]byte(" "), ltfsLabelMaximumBytes-len(first))...),
		[]byte(second),
	}
	facts, err := ParseLTFSLabels(labels)
	require.NoError(t, err)
	require.Equal(t, "30a91a08-daae-48d1-ae75-69804e61d2ea", facts.VolumeUUID)

	// Standard boolean and UUID representations compare by value, without defaulting absent policy.
	for _, boolean := range []struct{ first, second string }{{"true", "1"}, {"false", "0"}} {
		labels[0] = []byte(strings.Replace(ltfsLabelFixture("a", "a", "b"), "<compression>false</compression>",
			"<compression>"+boolean.first+"</compression>", 1))
		labels[1] = []byte(strings.ReplaceAll(strings.Replace(ltfsLabelFixture("b", "a", "b"),
			"<compression>false</compression>", "<compression>"+boolean.second+"</compression>", 1),
			"30a91a08-daae-48d1-ae75-69804e61d2ea", "30A91A08-DAAE-48D1-AE75-69804E61D2EA"))
		facts, err := ParseLTFSLabels(labels)
		require.NoError(t, err)
		require.Equal(t, boolean.first == "true", facts.Compression)
	}
}

func TestParseLTFSLabelsRejectInvalidRecord(t *testing.T) {
	// Invalid records must not yield a partial mapping or let extension content replace missing fields.
	valid := ltfsLabelFixture("a", "a", "b")
	replace := func(old, new string) string { return strings.Replace(valid, old, new, 1) }
	for _, test := range []struct {
		name, label, want string
	}{
		{"empty", "", "record size"},
		{"oversized", valid + strings.Repeat(" ", ltfsLabelMaximumBytes), "record size"},
		{"truncated", strings.TrimSuffix(valid, "</ltfslabel>"), "XML"},
		{"multiple roots", valid + valid, "one unqualified ltfslabel root"},
		{"wrong root", strings.ReplaceAll(valid, "ltfslabel", "ltfsindex"), "ltfslabel root"},
		{"namespaced root", replace("<ltfslabel ", "<ltfslabel xmlns=\"urn:other\" "), "ltfslabel root"},
		{"trailing data", valid + "garbage", "outside a scalar"},
		{"trailing NUL", valid + "\x00", "XML"},
		{"missing version", replace(" version=\"2.4.0\"", ""), "version"},
		{"duplicate version", replace("version=\"2.4.0\"", "version=\"\" version=\"2.4.0\""), "multiple versions"},
		{"unsupported version", replace("2.4.0", "3.0.0"), "version"},
		{"version overflow", replace("2.4.0", "2.100.0"), "version"},
		{"malformed version", replace("2.4.0", "2.4"), "version"},
		{"missing compression", replace("<compression>false</compression>", ""), "compression"},
		{"empty compression", replace("<compression>false</compression>", "<compression/>"), "compression"},
		{"invalid compression", replace("<compression>false</compression>",
			"<compression>FALSE</compression>"), "compression"},
		{"duplicate compression", replace("<compression>false</compression>",
			"<compression>false</compression><compression>true</compression>"), "repeated"},
		{"missing UUID", replace("<volumeuuid>30a91a08-daae-48d1-ae75-69804e61d2ea</volumeuuid>", ""), "volumeuuid"},
		{"hidden UUID", replace("<volumeuuid>30a91a08-daae-48d1-ae75-69804e61d2ea</volumeuuid>",
			"<extension><volumeuuid>30a91a08-daae-48d1-ae75-69804e61d2ea</volumeuuid></extension>"), "volumeuuid"},
		{"invalid UUID", replace("30a91a08-daae-48d1-ae75-69804e61d2ea", "not-a-uuid"), "UUID"},
		{"compact UUID", replace("30a91a08-daae-48d1-ae75-69804e61d2ea", "30a91a08daae48d1ae7569804e61d2ea"), "UUID"},
		{"URN UUID", replace("30a91a08-daae-48d1-ae75-69804e61d2ea",
			"urn:uuid:30a91a08-daae-48d1-ae75-69804e61d2ea"), "UUID"},
		{"nested scalar", replace("<blocksize>524288</blocksize>", "<blocksize><value>524288</value></blocksize>"), "nested"},
		{"missing block size", replace("<blocksize>524288</blocksize>", ""), "blocksize"},
		{"zero block size", replace("<blocksize>524288</blocksize>", "<blocksize>0</blocksize>"), "block size"},
		{"overflow block size", replace("<blocksize>524288</blocksize>", "<blocksize>4294967296</blocksize>"), "block size"},
		{"negative block size", replace("<blocksize>524288</blocksize>", "<blocksize>-1</blocksize>"), "block size"},
		{"missing local partition", replace("<location><partition>a</partition></location>", ""), "location/partition"},
		{"duplicate location", replace("<location><partition>a</partition></location>",
			"<location><partition>a</partition></location><location><partition>b</partition></location>"), "repeated"},
		{"duplicate role", replace("<index>a</index>", "<index>a</index><index>b</index>"), "repeated"},
		{"missing index role", replace("<index>a</index>", ""), "partitions/index"},
		{"missing data role", replace("<data>b</data>", ""), "partitions/data"},
		{"same roles", replace("<data>b</data>", "<data>a</data>"), "ambiguous"},
		{"unlisted local partition", replace("<location><partition>a</partition></location>",
			"<location><partition>c</partition></location>"), "ambiguous"},
		{"invalid local symbol", replace("<location><partition>a</partition></location>",
			"<location><partition>A</partition></location>"), "partition"},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts, err := ParseLTFSLabels([2][]byte{[]byte(test.label), []byte(ltfsLabelFixture("b", "a", "b"))})
			require.ErrorContains(t, err, test.want)
			require.Nil(t, facts)
		})
	}
}

func TestParseLTFSLabelsRejectDisagreement(t *testing.T) {
	// Both physically distinct records must describe the same initialization and roles.
	first, second := ltfsLabelFixture("a", "a", "b"), ltfsLabelFixture("b", "a", "b")
	for _, test := range []struct{ name, old, new string }{
		{"UUID", "30a91a08-daae-48d1-ae75-69804e61d2ea", "8b69a5d7-0d8c-4258-b2ae-c86da21d61d8"},
		{"block size", "524288", "262144"},
		{"compression", "<compression>false</compression>", "<compression>true</compression>"},
		{"roles", "<index>a</index><data>b</data>", "<index>b</index><data>a</data>"},
		{"format time", "2013-02-01T18:35:47.866846222Z", "2014-02-01T18:35:47.866846222Z"},
		{"creator", "LTFS 3.5.0 - Linux - mkltfs", "LTFS 3.4.0 - Linux - mkltfs"},
		{"version", "2.4.0", "2.3.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts, err := ParseLTFSLabels([2][]byte{[]byte(first), []byte(strings.Replace(second, test.old, test.new, 1))})
			require.ErrorContains(t, err, "disagree")
			require.Nil(t, facts)
		})
	}

	// Identical Labels cannot establish a bijection between two physical partitions and two symbols.
	facts, err := ParseLTFSLabels([2][]byte{[]byte(first), []byte(first)})
	require.ErrorContains(t, err, "repeat local partition")
	require.Nil(t, facts)
}

func ltfsLabelFixture(partition, index, data string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ltfslabel version="2.4.0">
  <creator>LTFS 3.5.0 - Linux - mkltfs</creator>
  <formattime>2013-02-01T18:35:47.866846222Z</formattime>
  <volumeuuid>30a91a08-daae-48d1-ae75-69804e61d2ea</volumeuuid>
  <location><partition>%s</partition></location>
  <partitions><index>%s</index><data>%s</data></partitions>
  <blocksize>524288</blocksize>
  <compression>false</compression>
</ltfslabel>`, partition, index, data)
}
