package test161

import (
	"github.com/stretchr/testify/assert"
	"math/rand"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	rand.Seed(time.Now().UTC().UnixNano())
	os.Exit(m.Run())
}

func TestRunBoot(t *testing.T) {
	assert := assert.New(t)

	test, err := TestFromString("q")
	assert.Nil(err)

	err = test.Run("./fixtures/", "")
	assert.Nil(err)

	assert.Equal(test.Commands[1].Env, "kernel")

	assert.Equal(test.Status, "shutdown")
	t.Log(test.OutputJSON())
	t.Log(test.OutputString())
}

func TestRunShell(t *testing.T) {
	assert := assert.New(t)

	test, err := TestFromString("s\n$ /bin/true\n$ exit\nq")
	assert.Nil(err)

	err = test.Run("./fixtures/", "")
	assert.Nil(err)

	assert.Equal(test.Commands[1].Env, "kernel")
	assert.Equal(test.Commands[2].Env, "shell")
	assert.Equal(test.Commands[3].Env, "shell")
	assert.Equal(test.Commands[4].Env, "kernel")

	assert.Equal(test.Status, "shutdown")
	t.Log(test.OutputJSON())
	t.Log(test.OutputString())
}

func TestRunPanic(t *testing.T) {
	assert := assert.New(t)

	test, err := TestFromString("panic")
	assert.Nil(err)

	err = test.Run("./fixtures/", "")
	assert.Nil(err)

	assert.Equal(test.Commands[1].Env, "kernel")

	assert.Equal(test.Status, "crash")

	t.Log(test.OutputJSON())
	t.Log(test.OutputString())
}