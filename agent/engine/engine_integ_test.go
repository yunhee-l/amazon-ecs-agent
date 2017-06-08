// +build integration
// Copyright 2014-2017 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package engine

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/amazon-ecs-agent/agent/api"
	"github.com/aws/amazon-ecs-agent/agent/config"
	"github.com/aws/amazon-ecs-agent/agent/credentials"
	"github.com/aws/amazon-ecs-agent/agent/ec2"
	"github.com/aws/amazon-ecs-agent/agent/engine/dockerclient"
	"github.com/aws/amazon-ecs-agent/agent/engine/dockerstate"
	"github.com/aws/amazon-ecs-agent/agent/eventstream"
	"github.com/aws/amazon-ecs-agent/agent/statemanager"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
)

const (
	testDockerStopTimeout  = 2 * time.Second
	credentialsIDIntegTest = "credsid"
)

func init() {
	// Set this very low for integ tests only
	_stoppedSentWaitInterval = 1 * time.Second
}

func createTestTask(arn string) *api.Task {
	return &api.Task{
		Arn:                 arn,
		Family:              arn,
		Version:             "1",
		DesiredStatusUnsafe: api.TaskRunning,
		Containers:          []*api.Container{createTestContainer()},
	}
}

func defaultTestConfigIntegTest() *config.Config {
	cfg, _ := config.NewConfig(ec2.NewBlackholeEC2MetadataClient())
	return cfg
}

func setupWithDefaultConfig(t *testing.T) (TaskEngine, func(), credentials.Manager) {
	return setup(defaultTestConfigIntegTest(), t)
}

func setup(cfg *config.Config, t *testing.T) (TaskEngine, func(), credentials.Manager) {
	if os.Getenv("ECS_SKIP_ENGINE_INTEG_TEST") != "" {
		t.Skip("ECS_SKIP_ENGINE_INTEG_TEST")
	}
	if !isDockerRunning() {
		t.Skip("Docker not running")
	}
	clientFactory := dockerclient.NewFactory(dockerEndpoint)
	dockerClient, err := NewDockerGoClient(clientFactory, cfg)
	if err != nil {
		t.Fatalf("Error creating Docker client: %v", err)
	}
	credentialsManager := credentials.NewManager()
	state := dockerstate.NewTaskEngineState()
	imageManager := NewImageManager(cfg, dockerClient, state)
	imageManager.SetSaver(statemanager.NewNoopStateManager())
	taskEngine := NewDockerTaskEngine(cfg, dockerClient, credentialsManager,
		eventstream.NewEventStream("ENGINEINTEGTEST", context.Background()), imageManager, state)
	taskEngine.Init()
	return taskEngine, func() {
		taskEngine.Shutdown()
	}, credentialsManager
}

func TestHostVolumeMount(t *testing.T) {
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()

	stateChangeEvents := taskEngine.StateChangeEvents()

	tmpPath, _ := ioutil.TempDir("", "ecs_volume_test")
	defer os.RemoveAll(tmpPath)
	ioutil.WriteFile(filepath.Join(tmpPath, "test-file"), []byte("test-data"), 0644)

	testTask := createTestHostVolumeMountTask(tmpPath)

	go taskEngine.AddTask(testTask)

	event := <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerRunning, "Expected container to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskRunning, "Expected task to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerStopped, "Expected container to be STOPPED")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskStopped, "Expected task to be STOPPED")

	assert.NotNil(t, testTask.Containers[0].KnownExitCode, "No exit code found")
	assert.Equal(t, 42, *testTask.Containers[0].KnownExitCode, "Wrong exit code")

	data, err := ioutil.ReadFile(filepath.Join(tmpPath, "hello-from-container"))
	assert.Nil(t, err, "Unexpected error")
	assert.Equal(t, "hi", strings.TrimSpace(string(data)), "Incorrect file contents")
}

func TestEmptyHostVolumeMount(t *testing.T) {
	taskEngine, done, _ := setupWithDefaultConfig(t)
	defer done()

	stateChangeEvents := taskEngine.StateChangeEvents()

	// creates a task with two containers
	testTask := createTestEmptyHostVolumeMountTask()

	go taskEngine.AddTask(testTask)

	event := <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerRunning, "Expected container to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerRunning, "Expected container to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskRunning, "Expected task to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerStopped, "Expected container to be STOPPED")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerStopped, "Expected container to be STOPPED")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskStopped, "Expected task to be STOPPED")

	assert.NotNil(t, testTask.Containers[0].KnownExitCode, "No exit code found")
	assert.Equal(t, 42, *testTask.Containers[0].KnownExitCode, "Wrong exit code, file probably wasn't present")
}

func TestSweepContainer(t *testing.T) {
	cfg := defaultTestConfigIntegTest()
	cfg.TaskCleanupWaitDuration = 1 * time.Minute
	taskEngine, done, _ := setup(cfg, t)
	defer done()

	stateChangeEvents := taskEngine.StateChangeEvents()

	testTask := createTestTask("testSweepContainer")

	go taskEngine.AddTask(testTask)

	event := <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerRunning, "Expected container to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskRunning, "Expected task to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerStopped, "Expected container to be STOPPED")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskStopped, "Expected task to be STOPPED")

	// Should be stopped, let's verify it's still listed...
	task, ok := taskEngine.(*DockerTaskEngine).State().TaskByArn("testSweepContainer")
	assert.True(t, ok, "Expected task to be present still, but wasn't")
	task.SetSentStatus(api.TaskStopped) // cleanupTask waits for TaskStopped to be sent before cleaning
	time.Sleep(1 * time.Minute)
	for i := 0; i < 60; i++ {
		_, ok = taskEngine.(*DockerTaskEngine).State().TaskByArn("testSweepContainer")
		if !ok {
			break
		}
		time.Sleep(1 * time.Second)
	}
	assert.False(t, ok, "Expected container to have been swept but was not")
}

// TestStartStopWithCredentials starts and stops a task for which credentials id
// has been set
func TestStartStopWithCredentials(t *testing.T) {
	taskEngine, done, credentialsManager := setupWithDefaultConfig(t)
	defer done()

	testTask := createTestTask("testStartWithCredentials")
	taskCredentials := credentials.TaskIAMRoleCredentials{
		IAMRoleCredentials: credentials.IAMRoleCredentials{CredentialsID: credentialsIDIntegTest},
	}
	credentialsManager.SetTaskCredentials(taskCredentials)
	testTask.SetCredentialsID(credentialsIDIntegTest)

	stateChangeEvents := taskEngine.StateChangeEvents()

	go taskEngine.AddTask(testTask)

	event := <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerRunning, "Expected container to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskRunning, "Expected task to be RUNNING")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.ContainerStateChange).Status, api.ContainerStopped, "Expected container to be STOPPED")

	event = <-stateChangeEvents
	assert.Equal(t, event.(api.TaskStateChange).Status, api.TaskStopped, "Expected task to be STOPPED")

	// When task is stopped, credentials should have been removed for the
	// credentials id set in the task
	_, ok := credentialsManager.GetTaskCredentials(credentialsIDIntegTest)
	assert.False(t, ok, "Credentials not removed from credentials manager for stopped task")
}
