package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/jay1999ke/test161"
	"gopkg.in/mgo.v2"
	yaml "gopkg.in/yaml.v2"
)

// Environment config
type SubmissionServerConfig struct {
	CacheDir         string                 `yaml:"cachedir"`
	Test161Dir       string                 `yaml:"test161dir"`
	OverlayDir       string                 `yaml:"overlaydir"`
	KeyDir           string                 `yaml:"keydir"`
	UsageDir         string                 `yaml:"usagedir"`
	MaxTests         uint                   `yaml:"max_tests"`
	Database         string                 `yaml:"db_name"`
	DBServers        []string               `yaml:"db_servers"`
	DBUser           string                 `yaml:"db_user"`
	DBReplicaSet     string                 `yaml:"db_replica_set"`
	DBPassword       string                 `yaml:"db_pw"`
	DBTimeout        uint                   `yaml:"db_timeout"`
	DBSSL            bool                   `yaml:"db_ssl"`
	APIPort          uint                   `yaml:"api_port"`
	MinClient        test161.ProgramVersion `yaml:"min_client"`
	StaffOnlyTargets []string               `yaml:"staff_only_targets"`
	DisabledTargets  []string               `yaml:"disabled_targets"`
}

const CONF_FILE = ".test161-server.conf"

var defaultConfig = &SubmissionServerConfig{
	CacheDir:         "/var/cache/test161/builds",
	Test161Dir:       "../fixtures/",
	MaxTests:         0,
	Database:         "test161",
	DBServers:        []string{"localhost:27017"},
	DBUser:           "",
	DBPassword:       "",
	DBTimeout:        10,
	APIPort:          4000,
	MinClient:        test161.ProgramVersion{},
	StaffOnlyTargets: []string{},
	DisabledTargets:  []string{},
}

var logger = log.New(os.Stderr, "test161-server: ", log.LstdFlags)

type SubmissionServer struct {
	conf          *SubmissionServerConfig
	env           *test161.TestEnvironment
	submissionMgr *test161.SubmissionManager
}

var submissionServer *SubmissionServer

func NewSubmissionServer() (test161Server, error) {

	conf, err := loadServerConfig()
	if err != nil {
		return nil, err
	}

	s := &SubmissionServer{
		conf: conf,
	}

	if err := s.setUpEnvironment(); err != nil {
		return nil, err
	}

	return s, nil
}

var minClientVer test161.ProgramVersion

// listTargets return all targets available to submit to
func listTargets(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", JsonHeader)
	w.WriteHeader(http.StatusOK)

	list := submissionServer.listTargets()

	if err := json.NewEncoder(w).Encode(list); err != nil {
		logger.Println("Error encoding target list:", err)
	}
}

func (s *SubmissionServer) listTargets() *test161.TargetList {
	return s.env.TargetList()
}

func submissionRequestFromHttp(w http.ResponseWriter, r *http.Request, validateOnly bool) *test161.SubmissionRequest {
	var request test161.SubmissionRequest

	body, err := ioutil.ReadAll(io.LimitReader(r.Body, 1*1024*1024))
	if err != nil {
		logger.Println("Error reading web request:", err)
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}

	if err := r.Body.Close(); err != nil {
		logger.Println("Error closing submission request body:", err)
		w.WriteHeader(http.StatusBadRequest)
	}

	if !validateOnly {
		logger.Println("Submission Request:", string(body))
	} else {
		logger.Println("Validation Request:", string(body))
	}

	if err := json.Unmarshal(body, &request); err != nil {
		w.Header().Set("Content-Type", JsonHeader)
		w.WriteHeader(http.StatusBadRequest)

		logger.Printf("Error unmarshalling submission request. Error: %v\nRequest: %v\n", err, string(body))
		if err := json.NewEncoder(w).Encode(err); err != nil {
			logger.Println("Encoding error:", err)
		}
		return nil
	}

	return &request
}

func (s *SubmissionServer) validateRequest(request *test161.SubmissionRequest) (int, error) {

	var err error

	// Check the client's version and make sure it's not too old
	if request.ClientVersion.CompareTo(s.conf.MinClient) < 0 {
		logger.Printf("Old request (version %v)\n", request.ClientVersion)
		err = errors.New("test161 version too old, test161-server requires version " + s.conf.MinClient.String())
		return http.StatusNotAcceptable, err
	}

	// Check to see if we're accepting submissions
	if s.submissionMgr.Status() == test161.SM_NOT_ACCEPTING {
		// We're trying to shut down
		logger.Println("Rejecting due to SM_NOT_ACCEPTING")
		err = errors.New("The submission server is currently not accepting new submissions")
		return http.StatusServiceUnavailable, err
	}

	// Validate the students and check if we're accepting staff-only submissions
	if students, err := request.Validate(s.env); err != nil {
		// Unprocessable entity
		return 422, err
	} else if err = s.checkStaffOnlySubmission(students); err != nil {
		return http.StatusServiceUnavailable, err
	} else if err = s.checkTargetBlacklists(students, request.Target); err != nil {
		return http.StatusServiceUnavailable, err
	}

	return http.StatusOK, nil
}

func (s *SubmissionServer) GetEnv() *test161.TestEnvironment {
	return s.env
}

func (s *SubmissionServer) checkStaffOnlySubmission(students []*test161.Student) error {
	if s.submissionMgr.Status() == test161.SM_STAFF_ONLY {
		for _, student := range students {
			if isStaff, _ := student.IsStaff(s.env); !isStaff {
				err := errors.New("The submission server is currently not accepting new submissions from students")
				return err
			}
		}
	}
	return nil
}

func (s *SubmissionServer) checkTargetBlacklists(students []*test161.Student, targetName string) error {
	for _, name := range s.conf.DisabledTargets {
		if name == targetName {
			return fmt.Errorf("The target '%v' is currently disabled on the server.", name)
		}
	}

	isStaff := true

	for _, student := range students {
		if isStaff, _ = student.IsStaff(s.env); !isStaff {
			break
		}
	}

	if !isStaff {
		for _, name := range s.conf.StaffOnlyTargets {
			if name == targetName {
				return fmt.Errorf("The target '%v' is currently disabled on the server for students.", name)
			}
		}
	}
	return nil
}

func (s *SubmissionServer) NewSubmission(request *test161.SubmissionRequest) (*test161.Submission, []error) {
	return test161.NewSubmission(request, s.env)
}

func (s *SubmissionServer) RunAsync(submission *test161.Submission) {
	// Run it!
	go func() {
		if err := s.submissionMgr.Run(submission); err != nil {
			logger.Println("Error running submission:", err)
		}
	}()
}

func (s *SubmissionServer) CheckUserKeys(request *test161.SubmissionRequest) []*test161.RequestKeyResonse {
	return request.CheckUserKeys(s.env)
}

// createSubmission accepts POST requests
func createSubmission(w http.ResponseWriter, r *http.Request) {

	request := submissionRequestFromHttp(w, r, false)
	if request == nil {
		return
	}

	// Validate with the submission server
	if response, err := submissionServer.validateRequest(request); err != nil {
		sendErrorCode(w, response, err)
		return
	}

	// Make sure we can create the submission.  This checks for everything but run errors.
	submission, errs := submissionServer.NewSubmission(request)

	if len(errs) > 0 {
		w.Header().Set("Content-Type", JsonHeader)
		w.WriteHeader(422) // unprocessable entity

		// Marhalling a slice of arrays doesn't work, so we'll send back strings.
		errorStrings := []string{}
		for _, e := range errs {
			errorStrings = append(errorStrings, fmt.Sprintf("%v", e))
		}

		if err := json.NewEncoder(w).Encode(errorStrings); err != nil {
			logger.Println("Encoding error:", err)
		}
		return
	}

	w.WriteHeader(http.StatusCreated)
	submissionServer.RunAsync(submission)
}

// validate accepts POST requests
func validateSubmission(w http.ResponseWriter, r *http.Request) {

	request := submissionRequestFromHttp(w, r, true)
	if request == nil {
		return
	}

	// Validate with the submission server
	if response, err := submissionServer.validateRequest(request); err != nil {
		sendErrorCode(w, response, err)
		return
	}

	keyInfo := submissionServer.CheckUserKeys(request)
	w.Header().Set("Content-Type", JsonHeader)
	w.WriteHeader(http.StatusOK)

	if len(keyInfo) > 0 {
		if err := json.NewEncoder(w).Encode(keyInfo); err != nil {
			logger.Println("Encoding error (Validate Response):", err)
		}
	}
}

// getStats returns the current manager statistics
func getStats(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", JsonHeader)
	w.WriteHeader(http.StatusOK)

	stats := submissionServer.submissionMgr.CombinedStats()

	if err := json.NewEncoder(w).Encode(stats); err != nil {
		logger.Println("Error encoding stats:", err)
	}
}

func apiUsage(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, `<html><body>See <a href="https://github.com/jay1999ke/test161">the ops-class test161 GitHub page </a> for API and usage</body></html>`)
}

type KeygenRequest struct {
	Email string
	Token string
}

func (s *SubmissionServer) KeyGen(request *KeygenRequest) (string, error) {
	return test161.KeyGen(request.Email, request.Token, s.env)
}

// Generate a public/private key pair for a particular user
func keygen(w http.ResponseWriter, r *http.Request) {
	var request KeygenRequest

	body, err := ioutil.ReadAll(io.LimitReader(r.Body, 2*1024))
	if err != nil {
		logger.Println("Error reading web request:", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := r.Body.Close(); err != nil {
		logger.Println("Error closing submission request body:", err)
		w.WriteHeader(http.StatusBadRequest)
	}

	if err := json.Unmarshal(body, &request); err != nil {
		logger.Printf("Error unmarshalling keygen request. Error: %v\nRequest: %v\n", err, string(body))
		sendErrorCode(w, http.StatusBadRequest, errors.New("Error unmarshalling keygen request."))
		return
	}

	key, err := submissionServer.KeyGen(&request)
	if err != nil {
		// Unprocessable entity
		sendErrorCode(w, 422, err)
	} else {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, key)
	}

}

func loadServerConfig() (*SubmissionServerConfig, error) {

	// Check current directory, but fall back to home directory
	search := []string{
		CONF_FILE,
		path.Join(os.Getenv("HOME"), CONF_FILE),
	}

	file := ""

	for _, f := range search {
		if _, err := os.Stat(f); err == nil {
			file = f
			break
		}
	}

	// Use defaults
	if file == "" {
		logger.Println("Using default server configuration")
		// TODO: Spit out the default config
		return defaultConfig, nil
	}

	data, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}

	conf := &SubmissionServerConfig{}
	err = yaml.Unmarshal(data, conf)

	if err != nil {
		return nil, err
	}

	return conf, nil
}

func (s *SubmissionServer) setUpEnvironment() error {
	// MongoDB connection
	mongoTestDialInfo := &mgo.DialInfo{
		Username:       s.conf.DBUser,
		Password:       s.conf.DBPassword,
		Database:       s.conf.Database,
		Addrs:          s.conf.DBServers,
		ReplicaSetName: s.conf.DBReplicaSet,
		Timeout:        time.Duration(s.conf.DBTimeout) * time.Second,
	}

	if s.conf.DBSSL {
		logger.Println("Initializing SSL...")
		mongoTestDialInfo.DialServer = func(addr *mgo.ServerAddr) (net.Conn, error) {
			return tls.Dial("tcp", addr.String(), &tls.Config{})
		}
	}

	logger.Println("Initializing connection to MongoDB...")
	mongo, err := test161.NewMongoPersistence(mongoTestDialInfo)
	if err != nil {
		return err
	}
	logger.Println("Connected to MongoDB.")

	// Submission environment
	env, err := test161.NewEnvironment(s.conf.Test161Dir, mongo)
	if err != nil {
		return err
	}

	env.CacheDir = s.conf.CacheDir
	env.OverlayRoot = s.conf.OverlayDir
	env.KeyDir = s.conf.KeyDir
	env.Log = logger

	usageFailDir = s.conf.UsageDir

	logger.Println("Min client ver:", s.conf.MinClient)

	// OK, we're good to go
	s.env = env
	s.submissionMgr = test161.NewSubmissionManager(s.env)

	return nil
}

func (s *SubmissionServer) Start() {
	// Kick off test161 submission server
	test161.SetManagerCapacity(s.conf.MaxTests)
	test161.StartManager()

	// Init upload handlers
	initUploadManagers()

	// Finally, start listening for internal API requests
	logger.Fatal(http.ListenAndServe(fmt.Sprintf(":%v", s.conf.APIPort), NewRouter()))
}

func (s *SubmissionServer) Stop() {
	test161.StopManager()
	s.env.Persistence.Close()
}
