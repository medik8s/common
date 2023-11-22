package events

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
)

const (

	// Event message format "medik8s <operator shortname> <message>"
	format = "medik8s %3s %s"
	// types
	eventTypeNormal  = "Normal"
	eventTypeWarning = "Warning"

	// reasons
	EventReasonRemediationCreated      = "RemediationCreated"
	EventReasonRemediationStoppedByNHC = "RemediationStoppedByNHC"
	EventReasonAddFinalizer            = "AddFinalizer"
	EventReasonRemoveFinalizer         = "RemoveFinalizer"
	EventReasonNodeRemediated          = "NodeRemediated"

	// shared between snr and far
	EventReasonDeleteResources      = "DeleteResources"
	EventReasonAddNoExecuteTaint    = "AddNoExecuteTaint"
	EventReasonRemoveNoExecuteTaint = "RemoveNoExecuteTaint"
	EventReasonNodeRebooted         = "NodeRebooted"

	// messages

	eventMessageRemediationCreated      = "Remediation was created"
	eventMessageRemediationStoppedByNHC = "Remediation was stopped by the Node Healthcheck Operator"
	eventMessageAddFinalizer            = "Finalizer was added"
	eventMessageRemoveFinalizer         = "Finalizer was removed"
	eventMessageNodeRemediated          = "Unhealthy node was remediated"

	// shared between snr and far
	eventMessageDeleteResources      = "Manually delete pods from the unhealthy node"
	eventMessageAddNoExecuteTaint    = "NoExecute taint was added"
	eventMessageRemoveNoExecuteTaint = "NoExecute taint was removed"
	eventMessageNodeRebooted         = "Unhealthy node was rebooted"

	eventMessageAddOutOfServiceTaint    = "Add out-of-service taint"
	eventMessageRemoveOutOfServiceTaint = "Remove out-of-service taint"

	// error
	unknownOperator= "UnknownOperator"
)

type EventRecorder struct {
	record.EventRecorder
	operator string
}


// EventGenerator generates an event for the far CR
func (e *EventRecorder) EventGenerator(obj runtime.Object, eventReason string) {
	eventType, eventMessage := EventCreator(eventReason, e.operator)
	e.Event(obj, eventType, eventReason, fmt.Sprintf(format, e.operator, eventMessage))
}

// EventCreator returns the evnetType and eventMessage based on the eventReason input
func EventCreator(eventReason, operatorName string) (string, string) {
	var eventMessage string
	eventType := eventTypeNormal
	switch eventReason {
	case EventReasonRemediationCreated:
		eventMessage = eventMessageRemediationCreated
	case EventReasonRemediationStoppedByNHC:
		eventMessage = eventMessageRemediationStoppedByNHC
	case EventReasonAddFinalizer:
		eventMessage = eventMessageAddFinalizer
	case EventReasonRemoveFinalizer:
		eventMessage = eventMessageRemoveFinalizer
	case EventReasonNodeRemediated:
		eventMessage = eventMessageNodeRemediated
	default:
		// try operator specefic  events
		switch operatorName{
		case "far":
			eventType, eventMessage = EventCreatorFar(eventReason)
		default :
			eventType, eventMessage = unknownOperator, unknownOperator
		}
	}
	return eventType, eventMessage
}

// EventCreatorFar returns the evnetType and eventMessage based on the eventReason input for far cr
func EventCreatorFar(eventReason string) (string, string) {
	var eventMessage string
	eventType := eventTypeNormal
	switch eventReason {
	case EventReasonRemoveNoExecuteTaint:
		eventMessage = eventMessageRemoveNoExecuteTaint
	case EventReasonAddNoExecuteTaint:
		eventMessage = eventMessageAddNoExecuteTaint
	case EventReasonNodeRebooted:
		eventMessage = eventMessageNodeRebooted
	case EventReasonDeleteResources:
		eventMessage = eventMessageDeleteResources
	default:
		eventType = eventTypeWarning
		eventMessage = "unknonwn event reason"
	}
	return eventType, eventMessage
}

