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

package execution

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh/terminal"

	"github.com/getgauge/gauge/env"
	"github.com/getgauge/gauge/execution/event"
	"github.com/getgauge/gauge/execution/result"
	"github.com/getgauge/gauge/gauge"
	"github.com/getgauge/gauge/gauge_messages"
	"github.com/getgauge/gauge/logger"
	"github.com/getgauge/gauge/plugin"
	"github.com/getgauge/gauge/runner"
	"github.com/getgauge/gauge/validation"
	"github.com/golang/protobuf/proto"
)

type scenarioExecutor struct {
	runner               runner.Runner
	pluginHandler        *plugin.Handler
	currentExecutionInfo *gauge_messages.ExecutionInfo
	stepExecutor         *stepExecutor
	errMap               *validation.ValidationErrMaps
	stream               int
}

func newScenarioExecutor(r runner.Runner, ph *plugin.Handler, ei *gauge_messages.ExecutionInfo, errMap *validation.ValidationErrMaps, stream int) *scenarioExecutor {
	return &scenarioExecutor{
		runner:               r,
		pluginHandler:        ph,
		currentExecutionInfo: ei,
		errMap:               errMap,
		stream:               stream,
	}
}

func (e *scenarioExecutor) execute(scenarioResult *result.ScenarioResult, scenario *gauge.Scenario, contexts []*gauge.Step, teardowns []*gauge.Step) {
	scenarioResult.ProtoScenario.Skipped = proto.Bool(false)
	if len(scenario.Steps) == 0 {
		e.skipSceForError(scenario, scenarioResult)
	}
	if vErrs, ok := e.errMap.ScenarioErrs[scenario]; ok {
		for _, vErr := range vErrs {
			if !vErr.IsUnimplementedError() {
				setSkipInfoInResult(scenarioResult, scenario, e.errMap)
				event.Notify(event.NewExecutionEvent(event.ScenarioStart, scenario, scenarioResult, e.stream, *e.currentExecutionInfo))
				event.Notify(event.NewExecutionEvent(event.ScenarioEnd, scenario, scenarioResult, e.stream, *e.currentExecutionInfo))
				return
			}
		}
	}
	event.Notify(event.NewExecutionEvent(event.ScenarioStart, scenario, scenarioResult, e.stream, *e.currentExecutionInfo))
	defer event.Notify(event.NewExecutionEvent(event.ScenarioEnd, scenario, scenarioResult, e.stream, *e.currentExecutionInfo))

	res := e.initScenarioDataStore()
	if res.GetFailed() {
		e.handleScenarioDataStoreFailure(scenarioResult, scenario, fmt.Errorf("Failed to initialize scenario datastore. Error: %s", res.GetErrorMessage()))
		return
	}

	e.notifyBeforeScenarioHook(scenarioResult)
	if !scenarioResult.GetFailed() {
		allSteps := append(contexts, append(scenario.Steps, teardowns...)...)
		protoContexts := scenarioResult.ProtoScenario.GetContexts()
		protoScenItems := scenarioResult.ProtoScenario.GetScenarioItems()
		protoTeardowns := scenarioResult.ProtoScenario.GetTearDownSteps()
		allItems := append(protoContexts, append(protoScenItems, protoTeardowns...)...)
		e.executeItems(allSteps, allItems, scenarioResult)
	}
	e.notifyAfterScenarioHook(scenarioResult)
	scenarioResult.UpdateExecutionTime()
}

func (e *scenarioExecutor) initScenarioDataStore() *gauge_messages.ProtoExecutionResult {
	initScenarioDataStoreMessage := &gauge_messages.Message{MessageType: gauge_messages.Message_ScenarioDataStoreInit.Enum(),
		ScenarioDataStoreInitRequest: &gauge_messages.ScenarioDataStoreInitRequest{}}
	return e.runner.ExecuteAndGetStatus(initScenarioDataStoreMessage)
}

func (e *scenarioExecutor) handleScenarioDataStoreFailure(scenarioResult *result.ScenarioResult, scenario *gauge.Scenario, err error) {
	logger.Errorf(err.Error())
	validationError := validation.NewValidationError(&gauge.Step{LineNo: scenario.Heading.LineNo, LineText: scenario.Heading.Value},
		err.Error(), e.currentExecutionInfo.CurrentSpec.GetFileName(), nil)
	e.errMap.ScenarioErrs[scenario] = []*validation.StepValidationError{validationError}
	setSkipInfoInResult(scenarioResult, scenario, e.errMap)
}

func (e *scenarioExecutor) skipSceForError(scenario *gauge.Scenario, scenarioResult *result.ScenarioResult) {
	errMsg := fmt.Sprintf("%s:%d No steps found in scenario", e.currentExecutionInfo.GetCurrentSpec().GetFileName(), scenario.Heading.LineNo)
	logger.Errorf(errMsg)
	validationError := validation.NewValidationError(&gauge.Step{LineNo: scenario.Heading.LineNo, LineText: scenario.Heading.Value},
		errMsg, e.currentExecutionInfo.GetCurrentSpec().GetFileName(), nil)
	e.errMap.ScenarioErrs[scenario] = []*validation.StepValidationError{validationError}
}

func setSkipInfoInResult(result *result.ScenarioResult, scenario *gauge.Scenario, errMap *validation.ValidationErrMaps) {
	result.ProtoScenario.Skipped = proto.Bool(true)
	var errors []string
	for _, err := range errMap.ScenarioErrs[scenario] {
		errors = append(errors, err.Error())
	}
	result.ProtoScenario.SkipErrors = errors
}

func (e *scenarioExecutor) notifyBeforeScenarioHook(scenarioResult *result.ScenarioResult) {
	message := &gauge_messages.Message{MessageType: gauge_messages.Message_ScenarioExecutionStarting.Enum(),
		ScenarioExecutionStartingRequest: &gauge_messages.ScenarioExecutionStartingRequest{CurrentExecutionInfo: e.currentExecutionInfo}}
	res := executeHook(message, scenarioResult, e.runner, e.pluginHandler)
	if res.GetFailed() {
		setScenarioFailure(e.currentExecutionInfo)
		handleHookFailure(scenarioResult, res, result.AddPreHook)
	}
}

func (e *scenarioExecutor) notifyAfterScenarioHook(scenarioResult *result.ScenarioResult) {
	message := &gauge_messages.Message{MessageType: gauge_messages.Message_ScenarioExecutionEnding.Enum(),
		ScenarioExecutionEndingRequest: &gauge_messages.ScenarioExecutionEndingRequest{CurrentExecutionInfo: e.currentExecutionInfo}}
	res := executeHook(message, scenarioResult, e.runner, e.pluginHandler)
	if res.GetFailed() {
		setScenarioFailure(e.currentExecutionInfo)
		handleHookFailure(scenarioResult, res, result.AddPostHook)
	}
}

func (e *scenarioExecutor) canExecuteManually() bool {
	enabled := strings.ToLower(os.Getenv(env.ManualExecutionEnabled)) == "true"
	return enabled && !InParallel && terminal.IsTerminal(int(os.Stdout.Fd()))
}

func (e *scenarioExecutor) executeItems(items []*gauge.Step, protoItems []*gauge_messages.ProtoItem, scenarioResult *result.ScenarioResult) {
	var itemsIndex int
	for _, protoItem := range protoItems {
		if protoItem.GetItemType() == gauge_messages.ProtoItem_Concept || protoItem.GetItemType() == gauge_messages.ProtoItem_Step {
			var failed, recoverable bool
			if e.canExecuteManually() {
				if stepErr, ok := e.errMap.StepErrs[items[itemsIndex]]; ok && stepErr.IsUnimplementedError() {
					fmt.Printf("\n\nHybrid execution of: %s\n", items[itemsIndex].GetLineText())
					fmt.Println("Enter [P] for passed step, [F] for failed step (default).")
					input := "F"
					fmt.Scanln(&input)
					failed = input == "F"
					if failed {
						fmt.Println("Halt scenario execution? (Y/N)")
						fmt.Scanln(&input)
						recoverable = input == "Y"
					}
					fmt.Println("Enter any additional messages, <ctrl-]> to exit")
					scn := bufio.NewScanner(os.Stdin)
					var messages []string
					for scn.Scan() {
						line := scn.Text()
						messages = append(messages, line)
						if len(line) > 0 && line[len(line)-1] == '\x1D' {
							line = line[0 : len(line)-1]
							break
						}
					}
					executionResult := &gauge_messages.ProtoExecutionResult{Message: messages,
						Failed:           proto.Bool(failed),
						RecoverableError: proto.Bool(recoverable),
						ExecutionTime:    proto.Int64(0)}
					protoItem.GetStep().StepExecutionResult = &gauge_messages.ProtoStepExecutionResult{
						ExecutionResult: executionResult, Skipped: proto.Bool(false)}
				} else {
					failed, recoverable = e.executeItem(items[itemsIndex], protoItem, scenarioResult)
				}
				itemsIndex++
				if failed {
					scenarioResult.SetFailure()
					if !recoverable {
						return
					}
				}
			}
		}
	}
}

func (e *scenarioExecutor) executeItem(item *gauge.Step, protoItem *gauge_messages.ProtoItem, scenarioResult *result.ScenarioResult) (failed bool, recoverable bool) {
	if protoItem.GetItemType() == gauge_messages.ProtoItem_Concept {
		protoConcept := protoItem.GetConcept()
		res := e.executeConcept(item, protoConcept, scenarioResult)
		failed = res.GetFailed()
		recoverable = res.GetRecoverable()
	} else if protoItem.GetItemType() == gauge_messages.ProtoItem_Step {
		se := &stepExecutor{runner: e.runner, pluginHandler: e.pluginHandler, currentExecutionInfo: e.currentExecutionInfo, stream: e.stream}
		res := se.executeStep(item, protoItem.GetStep())
		protoItem.GetStep().StepExecutionResult = res.ProtoStepExecResult()
		failed = res.GetFailed()
		recoverable = res.ProtoStepExecResult().GetExecutionResult().GetRecoverableError()
	}
	return
}

func (e *scenarioExecutor) executeConcept(item *gauge.Step, protoConcept *gauge_messages.ProtoConcept, scenarioResult *result.ScenarioResult) *result.ConceptResult {
	cptResult := result.NewConceptResult(protoConcept)
	event.Notify(event.NewExecutionEvent(event.ConceptStart, item, nil, e.stream, *e.currentExecutionInfo))
	defer event.Notify(event.NewExecutionEvent(event.ConceptEnd, nil, cptResult, e.stream, *e.currentExecutionInfo))

	var conceptStepIndex int
	for _, protoStep := range protoConcept.Steps {
		if protoStep.GetItemType() == gauge_messages.ProtoItem_Concept || protoStep.GetItemType() == gauge_messages.ProtoItem_Step {
			failed, recoverable := e.executeItem(item.ConceptSteps[conceptStepIndex], protoStep, scenarioResult)
			conceptStepIndex++
			if failed {
				scenarioResult.SetFailure()
				cptResult.UpdateConceptExecResult()
				if recoverable {
					continue
				}
				return cptResult
			}
		}
	}
	cptResult.UpdateConceptExecResult()
	return cptResult
}

func setStepFailure(executionInfo *gauge_messages.ExecutionInfo) {
	setScenarioFailure(executionInfo)
	executionInfo.CurrentStep.IsFailed = proto.Bool(true)
}

func getParameters(fragments []*gauge_messages.Fragment) (parameters []*gauge_messages.Parameter) {
	for _, fragment := range fragments {
		if fragment.GetFragmentType() == gauge_messages.Fragment_Parameter {
			parameters = append(parameters, fragment.GetParameter())
		}
	}
	return parameters
}

func setScenarioFailure(executionInfo *gauge_messages.ExecutionInfo) {
	setSpecFailure(executionInfo)
	executionInfo.CurrentScenario.IsFailed = proto.Bool(true)
}
