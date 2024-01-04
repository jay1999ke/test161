package test161

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"

	"dario.cat/mergo"
	"github.com/ericaro/frontmatter"
	uuid "github.com/kevinburke/go.uuid"
)

// Many of the values below that come in from YAML are string types. This
// allows us to work around Go not having a nil type for things like ints, and
// also allows us to accept values like "10M" as numbers. Non-string types are
// only used when the default doesn't make sense. Given that the ultimate
// destination of these values is strings (sys161.conf configuration or JSON
// output) it doesn't matter.

type Sys161Conf struct {
	Path   string   `yaml:"path" json:"path"`
	CPUs   uint     `yaml:"cpus" json:"cpus"`
	RAM    string   `yaml:"ram" json:"ram"`
	Disk1  DiskConf `yaml:"disk1" json:"disk1"`
	Disk2  DiskConf `yaml:"disk2" json:"disk2"`
	Random uint32   `yaml:"-" json:"randomseed" bson:"randomseed"`
}

type DiskConf struct {
	Enabled string `yaml:"enabled" json:"enabled"`
	RPM     uint   `yaml:"rpm" json:"rpm"`
	Bytes   string `yaml:"bytes" json:"bytes"`
	NoDoom  string `yaml:"nodoom" json:"nodoom"`
}

type StatConf struct {
	Resolution float32 `yaml:"resolution" json:"resolution"`
	Window     uint    `yaml:"window" json:"window"`
}

type MonitorConf struct {
	Enabled         string  `yaml:"enabled" json:"enabled"`
	Window          uint    `yaml:"window" json:"window"`
	Kernel          Limits  `yaml:"kernel" json:"kernel"`
	User            Limits  `yaml:"user" json:"user"`
	ProgressTimeout float32 `yaml:"progresstimeout" json:"progresstimeout"`
	CommandTimeout  float32 `yaml:"commandtimeout" json:"commandtimeout"`
}

type Limits struct {
	EnableMin string  `yaml:"enablemin" json:"enablemin"`
	Min       float64 `yaml:"min" json:"min"`
	Max       float64 `yaml:"max" json:"max"`
}

type MiscConf struct {
	CommandRetries   uint    `yaml:"commandretries" json:"commandretries"`
	PromptTimeout    float32 `yaml:"prompttimeout" json:"prompttimeout"`
	CharacterTimeout uint    `yaml:"charactertimeout" json:"charactertimeout"`
	TempDir          string  `yaml:"tempdir" json:"-" bson:"-"`
	RetryCharacters  string  `yaml:"retrycharacters" json:"retrycharacters"`
	KillOnExit       string  `yaml:"killonexit" json:"killonexit"`
}

type CommandConf struct {
	Prefix string `yaml:"prefix" json:"prefix"`
	Prompt string `yaml:"prompt" json:"prompt"`
	Start  string `yaml:"start" json:"start"`
	End    string `yaml:"end" json:"end"`
}

var CONF_DEFAULTS = Test{
	Sys161: Sys161Conf{
		Path: "sys161",
		CPUs: 8,
		RAM:  "1M",
		Disk1: DiskConf{
			Enabled: "false",
			Bytes:   "32M",
			RPM:     7200,
			NoDoom:  "true",
		},
		Disk2: DiskConf{
			Enabled: "false",
			Bytes:   "32M",
			RPM:     7200,
			NoDoom:  "false",
		},
	},
	Stat: StatConf{
		Resolution: 0.01,
		Window:     1,
	},
	Monitor: MonitorConf{
		Enabled: "true",
		Window:  400,
		Kernel: Limits{
			EnableMin: "false",
			Min:       0.001,
			Max:       1.0,
		},
		User: Limits{
			EnableMin: "false",
			Min:       0.0001,
			Max:       1.0,
		},
		ProgressTimeout: 10.0,
		CommandTimeout:  60.0,
	},
	Misc: MiscConf{
		CommandRetries:   5,
		PromptTimeout:    1800.0,
		CharacterTimeout: 1000,
		RetryCharacters:  "true",
		KillOnExit:       "false",
	},
}

func confFromString(data string) (*Test, error) {
	t := &Test{}

	err := frontmatter.Unmarshal([]byte(data), t)
	if err != nil {
		return nil, err
	}
	t.Sys161.Random = rand.Uint32() >> 16

	t.requiredBy = make(map[string]bool)

	// TODO: Error checking here

	return t, nil
}

// TestFromFile parses the test file and sets configuration defaults.
func TestFromFile(filename string) (*Test, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("Error reading file %v: %v", filename, err)
	}
	test, err := TestFromString(string(data))
	if err != nil {
		err = fmt.Errorf("Error loading test file %v: %v", filename, err)
	}
	return test, err
}

// TestFromFile parses the test string and sets configuration defaults.
func TestFromString(data string) (*Test, error) {

	t, err := confFromString(data)
	if err != nil {
		return nil, err
	}

	// Check for empty commands and expand syntatic sugar before getting
	// started. Doing this first makes the main loop and retry logic simpler.

	t.ID = uuid.NewV4().String()
	err = t.initCommands()
	if err != nil {
		return nil, err
	}

	t.Result = TEST_RESULT_NONE

	return t, nil
}

func (t *Test) MergeConf(defaults Test) error {
	return mergo.Map(t, defaults)
}

const SYS161_TEMPLATE = `0 serial
1	emufs
{{if eq .Disk1.Enabled "true"}}
2	disk rpm={{.Disk1.RPM}} file=LHD0.img {{if eq .Disk1.NoDoom "true"}}nodoom{{end}} # bytes={{.Disk1.Bytes }}
{{end}}
{{if eq .Disk2.Enabled "true"}}
3	disk rpm={{.Disk2.RPM}} file=LHD1.img {{if eq .Disk2.NoDoom "true"}}nodoom{{end}} # bytes={{.Disk2.Bytes }}
{{end}}
28	random seed={{.Random}}
29	timer
30	trace
31	mainboard ramsize={{.RAM}} cpus={{.CPUs}}`

var confTemplate *template.Template
var confOnce sync.Once

// PrintConf formats the test configuration for use by sys161 via the sys161.conf file.
func (t *Test) PrintConf() (string, error) {

	confOnce.Do(func() { confTemplate, _ = template.New("conf").Parse(SYS161_TEMPLATE) })

	buffer := new(bytes.Buffer)

	// Can't fail, even if the test isn't properly initialized, because
	// t.Sys161 matches the template
	confTemplate.Execute(buffer, t.Sys161)

	var confString string
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if strings.TrimSpace(line) != "" {
			confString += line + "\n"
		}
	}
	return confString, nil
}

func (t *Test) confEqual(t2 *Test) bool {
	return t.Sys161 == t2.Sys161 &&
		t.Stat == t2.Stat &&
		t.Monitor == t2.Monitor &&
		t.Misc == t2.Misc &&
		reflect.DeepEqual(t.CommandConf, t2.CommandConf)
}

var prefixRegexp *regexp.Regexp
var prefixOnce sync.Once

func splitPrefix(commandLine string) (string, string) {
	prefixOnce.Do(func() { prefixRegexp = regexp.MustCompile(`^([!@#$%^&*]) `) })
	matches := prefixRegexp.FindStringSubmatch(strings.TrimSpace(commandLine))
	if len(matches) == 0 {
		return "", strings.TrimSpace(commandLine)
	} else {
		return matches[1], strings.TrimSpace(commandLine[1:])
	}
}

func (t *Test) checkCommandConf() error {
	prefixes := "$"
	errorMsg := ""
	var commandConf CommandConf
	for _, commandConf = range t.CommandConf {
		if commandConf.Prefix == "" || commandConf.Prompt == "" ||
			commandConf.Start == "" || commandConf.End == "" {
			errorMsg = "need to specific command prefix, prompt, start, and end"
			break
		}
		if len(commandConf.Prefix) > 1 {
			errorMsg = "illegal multicharacter prefix"
			break
		}
		prefix, _ := splitPrefix(commandConf.Prefix + " t")
		if prefix == "" {
			errorMsg = "invalid prefix"
			break
		} else if prefix == "$" {
			errorMsg = "the $ prefix is reserved for the shell"
			break
		} else if strings.ContainsAny(prefixes, prefix) {
			errorMsg = "duplicate prefix"
			break
		}
		prefixes += prefix
		startPrefix, _ := splitPrefix(commandConf.Start)
		if startPrefix != "" && startPrefix == prefix {
			errorMsg = "command start cannot start with own prefix"
			break
		}
		endPrefix, _ := splitPrefix(commandConf.End)
		if endPrefix != "" {
			errorMsg = "command exits should not contain a prefix"
			break
		}
	}
	if errorMsg == "" {
		for _, commandConf = range t.CommandConf {
			startPrefix, _ := splitPrefix(commandConf.Start)
			if startPrefix != "" && !strings.ContainsAny(prefixes, startPrefix) {
				errorMsg = "command start contains an unknown prefix"
				break
			}
		}
	}
	if errorMsg != "" {
		return errors.New(fmt.Sprintf("test161: %s (%v)", errorMsg, commandConf))
	} else {
		return nil
	}
}

var KERNEL_COMMAND_CONF = &CommandConf{
	Prompt: `OS/161 kernel [? for menu]: `,
	End:    "q",
}
var SHELL_COMMAND_CONF = &CommandConf{
	Prefix: "$",
	Prompt: `OS/161$ `,
	Start:  "s",
	End:    "exit",
}

func (t *Test) commandConfFromLine(commandLine string) (string, *CommandConf) {
	prefix, commandLine := splitPrefix(commandLine)
	if prefix == "" {
		return commandLine, KERNEL_COMMAND_CONF
	} else if prefix == "$" {
		return commandLine, SHELL_COMMAND_CONF
	} else {
		for _, commandConf := range t.CommandConf {
			if commandConf.Prefix == prefix {
				return commandLine, &commandConf
			}
		}
	}
	return commandLine, nil
}

// simplePrefixes handle prefixes that don't change the environment or require
// special prompts.

var khuPrefixRegexp *regexp.Regexp
var multiplierPrefixRegexp *regexp.Regexp
var simplePrefixOnce sync.Once

func simplePrefixes(inputCommands []string) ([]string, error) {
	outputCommands, err := doSimplePrefixes(inputCommands)
	if err != nil {
		return nil, err
	}
	outputCommands, err = doSimplePrefixes(outputCommands)
	if err != nil {
		return nil, err
	}
	return outputCommands, nil
}

func doSimplePrefixes(inputCommands []string) ([]string, error) {
	simplePrefixOnce.Do(func() {
		khuPrefixRegexp = regexp.MustCompile(`^\|(.*)`)
		multiplierPrefixRegexp = regexp.MustCompile(`^(\d+)x(.*)`)
	})
	outputCommands := make([]string, 0, len(inputCommands))
	for _, command := range inputCommands {
		command = strings.TrimSpace(command)
		matches := khuPrefixRegexp.FindStringSubmatch(command)
		if len(matches) != 0 {
			outputCommands = append(outputCommands, "khu", matches[1], "khu")
			continue
		}
		matches = multiplierPrefixRegexp.FindStringSubmatch(command)
		if len(matches) != 0 {
			multiplier, err := strconv.Atoi(matches[1])
			if err != nil {
				return nil, err
			}
			for i := 0; i < multiplier; i++ {
				outputCommands = append(outputCommands, matches[2])
			}
			continue
		}
		outputCommands = append(outputCommands, command)
	}
	return outputCommands, nil
}

// Don't get stuck in an infinite loop

const MAX_EXPANSION_LOOPS = 1024

func (c *Command) setCommandConfig(test *Test) {
	myname := c.Id()
	for _, tmpl := range test.CommandOverrides {
		if tmpl.Name == myname {
			c.Config = *(tmpl)
			return
		}
	}
}

func (t *Test) initCommands() (err error) {

	// Check defined command prefixes
	err = t.checkCommandConf()
	if err != nil {
		return err
	}

	// Set up the command configuration stack
	var commandConfStack []*CommandConf
	commandConfStack = append(commandConfStack, KERNEL_COMMAND_CONF)

	// Set the boot command
	t.Commands = append(t.Commands, &Command{
		Type:          "kernel",
		ID:            uuid.NewV4().String(),
		Test:          t,
		PromptPattern: regexp.MustCompile(regexp.QuoteMeta(KERNEL_COMMAND_CONF.Prompt)),
		Input: InputLine{
			Line: "boot",
		},
		Status:   COMMAND_STATUS_NONE,
		Panic:    CMD_OPT_NO,
		TimesOut: CMD_OPT_NO,
		Timeout:  0.0,
	})

	// Set all confs including kernel and shell
	allConfs := append(t.CommandConf, *SHELL_COMMAND_CONF, *KERNEL_COMMAND_CONF)

	commandLines := strings.Split(strings.TrimSpace(t.Content), "\n")
	commandLines, err = simplePrefixes(commandLines)
	if err != nil {
		return err
	}

	for i := 0; i <= MAX_EXPANSION_LOOPS; i++ {
		if i == MAX_EXPANSION_LOOPS {
			return errors.New("test161: infinite loop expanding command list")
		}
		if len(commandConfStack) == 0 {
			if len(commandLines) != 0 {
				return errors.New("test161: premature exit in command list")
			}
			// This is normal exit
			break
		}
		currentConf := commandConfStack[0]

		// May need to clean up previous configurations
		if len(commandLines) == 0 {
			commandLines = []string{strings.TrimSpace(currentConf.Prefix + " " + currentConf.End)}
			continue
		}

		// Peek at the first command
		commandLine := strings.TrimSpace(commandLines[0])
		if commandLine == "" {
			return errors.New("test161: found empty command")
		}

		commandLine, commandConf := t.commandConfFromLine(commandLine)
		if commandConf == nil {
			return errors.New(fmt.Sprintf("test161: command with invalid prefix %v", commandLine))
		}

		if !reflect.DeepEqual(currentConf, commandConf) {
			found := false
			for _, search := range commandConfStack {
				if commandConf == search {
					found = true
					break
				}
			}
			if found {
				commandLines = append([]string{strings.TrimSpace(currentConf.Prefix + " " + currentConf.End)}, commandLines...)
			} else {
				commandLines = append([]string{commandConf.Start}, commandLines...)
			}
			continue
		} else {
			typeConf := currentConf
			nextConf := currentConf
			if commandLine == currentConf.End {
				// The command exits the current configuration
				commandConfStack = commandConfStack[1:]
				if len(commandConfStack) > 0 {
					nextConf = commandConfStack[0]
				} else {
					nextConf = nil
				}
			} else {
				for _, search := range allConfs {
					if search.Start == strings.TrimSpace(currentConf.Prefix+" "+commandLine) {
						// The command starts a new configuration
						commandConfStack = append([]*CommandConf{&search}, commandConfStack...)
						typeConf = commandConfStack[0]
						nextConf = commandConfStack[0]
						break
					}
				}
			}
			var commandType string
			if typeConf.Prefix != "" || strings.HasPrefix(commandLine, "p ") {
				commandType = "user"
			} else {
				commandType = "kernel"
			}
			var promptPattern *regexp.Regexp
			if nextConf != nil {
				promptPattern = regexp.MustCompile(regexp.QuoteMeta(nextConf.Prompt))
			}
			newCmd := &Command{
				Type:          commandType,
				ID:            uuid.NewV4().String(),
				Test:          t,
				PromptPattern: promptPattern,
				Input: InputLine{
					Line: commandLine,
				},
				Status:   COMMAND_STATUS_NONE,
				Panic:    CMD_OPT_NO,
				TimesOut: CMD_OPT_NO,
				Timeout:  0.0,
			}
			newCmd.setCommandConfig(t)
			t.Commands = append(t.Commands, newCmd)
			commandLines = commandLines[1:]
		}
	}

	return nil
}
