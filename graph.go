// Copyright 2015 ThoughtWorks, Inc.

// This file is part of Gauge.

// Gauge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// Gauge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with Gauge.  If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"github.com/getgauge/gauge/parser"
	"fmt"
	"github.com/getgauge/gauge/gauge"
	"encoding/json"
	"strings"
	"github.com/getgauge/gauge-ruby/tmp/src/github.com/getgauge/common"
	"os"
	"path"
)

type node struct {
	stepText       string
	parsedStepText string
	nodes          []node
	duplicates     int
}

func (n node) getJSON() string {
	if len(n.nodes) == 0 {
		return fmt.Sprintf(`{"text": "%s", "parsedText": "%s", "times": %d, "nodes": %s}`,
			strings.Replace(n.stepText, "\"", "\\\"", -1),
			n.parsedStepText,
			n.duplicates,
			"[]")
	} else {
		json := ""
		for _, node := range n.nodes {
			json = json + "," + node.getJSON()
		}
		return fmt.Sprintf(`{"text": "%s", "parsedText": "%s", "times": %d, "nodes": %s}`,
			strings.Replace(n.stepText, "\"", "\\\"", -1),
			n.parsedStepText,
			n.duplicates,
			"[" + json[1:] + "]")
	}
}

func (n node) MarshalJSON() ([]byte, error) {
	return []byte(n.getJSON()), nil
}

func prepareGraph(specsDir string) {
	cd, _ := parser.CreateConceptsDictionary(true, []string{specsDir})
	specs, _ := parser.FindSpecs(specsDir, cd)
	nodes := make([]node, 0)
	for _, spec := range specs {
		for _, scn := range spec.Scenarios {
			steps := scn.Steps
			if len(spec.Contexts) > 0 {
				steps = append(spec.Contexts, scn.Steps...)
			}
			nodes = start(nodes, steps)
		}
	}
	printNodes(nodes, "")
	b, err := json.Marshal(nodes)
	if err != nil {
		fmt.Println("err: ", err)
	}
	wd, _ := os.Getwd()
	skelPath, err := common.GetSkeletonFilePath("graph")
	common.MirrorDir(skelPath, path.Join(wd, "graph"))
	file, _ := os.Create(path.Join(wd, "graph", "data.js"))
	file.Write([]byte("var nodeData = " + string(b)))
}

func start(nodes []node, steps []*gauge.Step) []node {
	index := indexOf(nodes, steps[0].Value)
	if index < 0 {
		nodes = append(nodes, node{stepText: steps[0].LineText, parsedStepText: steps[0].Value, nodes: make([]node, 0), duplicates: 1})
		index = len(nodes) - 1
	} else {
		nodes[index].duplicates++
	}
	nodes = createGraphFor(1, steps, index, nodes)
	return nodes
}

func createGraphFor(stepsIndex int, steps []*gauge.Step, nodeIndex int, nodes []node) []node {
	if stepsIndex >= len(steps) {
		return nodes
	}
	nIndex := indexOf(nodes[nodeIndex].nodes, steps[stepsIndex].Value)
	if nIndex < 0 {
		nodes[nodeIndex].nodes = append(nodes[nodeIndex].nodes, node{stepText: steps[stepsIndex].LineText, parsedStepText: steps[stepsIndex].Value, nodes: make([]node, 0), duplicates: 1})
		nIndex = len(nodes[nodeIndex].nodes) - 1
	} else {
		nodes[nodeIndex].nodes[nIndex].duplicates++
	}
	if stepsIndex < len(steps) - 1 {
		stepsIndex++
		createGraphFor(stepsIndex, steps, nIndex, nodes[nodeIndex].nodes)
	}
	return nodes
}

func indexOf(nodes []node, text string) int {
	for i, node := range nodes {
		if node.parsedStepText == text {
			return i
		}
	}
	return -1
}

func printNodes(nodes []node, indent string) {
	for _, node := range nodes {
		fmt.Println(indent, node.parsedStepText, " : ", node.duplicates)
		if len(node.nodes) > 0 {
			printNodes(node.nodes, indent + "\t")
		}
	}
}