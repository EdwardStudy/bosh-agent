package script

import (
	boshdrain "github.com/cloudfoundry/bosh-agent/agent/script/drain"
	boshsys "github.com/cloudfoundry/bosh-utils/system"
)

//go:generate counterfeiter . JobScriptProvider

type JobScriptProvider interface {
	NewScript(jobName string, scriptName string) Script
	NewDrainScript(jobName string, params boshdrain.ScriptParams) CancellableScript
	NewParallelScript(scriptName string, scripts []Script) CancellableScript
}

//go:generate counterfeiter . Script

type Script interface {
	Tag() string
	Path() string

	Exists() bool
	Run() error
	RunAsync() (boshsys.Process, boshsys.File, boshsys.File, error)
}

type CancellableScript interface {
	Script
	Cancel() error
}
