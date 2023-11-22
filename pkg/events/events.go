package events

const (

	// types
	EventTypeNormal  = "Normal"
	EventTypeWarning = "Warning"

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

	RemediationProccessedPrefix = "Remediation process"
	MessagePrefixFAR = "medik8s-far "
	MessagePrefixSNR = "medik8s-snr "
	MessagePrefixMDR = "medik8s-mdr "

	EventMessageRemediationCreated      = "Remediation was created"
	EventMessageRemediationStoppedByNHC = "Remediation was stopped by the Node Healthcheck Operator"
	EventMessageAddFinalizer            = "Finalizer was added"
	EventMessageRemoveFinalizer         = "Finalizer was removed"
	EventMessageNodeRemediated          = "Unhealthy node was remediated"

	// shared between snr and far
	EventMessageDeleteResources      = "Manually delete pods from the unhealthy node"
	EventMessageAddNoExecuteTaint    = "NoExecute taint was added"
	EventMessageRemoveNoExecuteTaint = "NoExecute taint was removed"
	EventMessageNodeRebooted         = "Unhealthy node was rebooted"

	EventMessageAddOutOfServiceTaint    = "Add out-of-service taint"
	EventMessageRemoveOutOfServiceTaint = "Remove out-of-service taint"
)
