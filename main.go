package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sts"
)

const (
	iamRoleName        = "dummy-iamrole"
	imdsCredentialPath = "/latest/meta-data/iam/security-credentials/"
	durationSeconds    = 1200
	updatableSeconds   = 600
	checkInterval      = 60
)

var (
	sourceProfile   = os.Getenv("IMDS_SOURCE_PROFILE")
	roleArn         = os.Getenv("IMDS_ROLE_ARN")
	roleSessionName string
	is              imdsState
	mu              sync.Mutex
)

type imdsState struct {
	creds           creds
	updatedUnixTime int64
}

type creds struct {
	Code        string  `json:"Code"`            // "Success"
	LastUpdated utcTime `json:"LastUpdated"`     // "2020-07-05T09:09:43Z"
	Type        string  `json:"Type"`            // "AWS-HMAC"
	AccessKey   string  `json:"AccessKeyId"`     // "ASIA6F6FQ3BJP2~~~~",
	SecretKey   string  `json:"SecretAccessKey"` // "W28OLWkl7U4qol~~~~",
	Token       string  `json:"Token"`           // "FwoGZXIvYXdzEN~~~~"
	Expiration  utcTime `json:"Expiration"`      // "2020-07-05T15:21:15Z"
}

type utcTime struct {
	time.Time
}

func (u utcTime) format() string {
	return u.Time.UTC().Format("2006-01-02T15:04:05Z")
}

func (u utcTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + u.format() + `"`), nil
}

func getCredentials() (retCreds creds) {
	input := sts.AssumeRoleInput{
		DurationSeconds: aws.Int64(durationSeconds),
		RoleArn:         aws.String(roleArn),
		RoleSessionName: aws.String(roleSessionName),
	}

	output, err := createSTSClient().AssumeRole(&input)
	if err != nil {
		log.Println(err)
		return retCreds
	}

	now := time.Now()
	retCreds.LastUpdated = utcTime{now}
	retCreds.Expiration = utcTime{now.Add(durationSeconds * time.Second)}
	retCreds.AccessKey = *output.Credentials.AccessKeyId
	retCreds.SecretKey = *output.Credentials.SecretAccessKey
	retCreds.Token = *output.Credentials.SessionToken
	retCreds.Code = "Success"
	retCreds.Type = "AWS-HMAC"
	return
}

func createSTSClient() *sts.STS {
	return sts.New(createSession())
}

func getIAMUsername() string {
	output, err := createSTSClient().GetCallerIdentity(&sts.GetCallerIdentityInput{})
	if err != nil {
		log.Println(err)
		return ""
	}

	return regexp.MustCompile(`.*\/`).ReplaceAllString(*output.Arn, "")
}

func createSession() *session.Session {
	creds := credentials.NewSharedCredentials("", sourceProfile)
	config := aws.Config{Credentials: creds}
	session, err := session.NewSession(&config)

	if err != nil {
		log.Fatal("Error creating session", err)
	}
	return session
}

func defaultHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, imdsCredentialPath, http.StatusFound)
}

func rolenameHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, iamRoleName)
}

func credentialHandler(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	jsonByte, err := json.Marshal(is.creds)
	mu.Unlock()
	if err != nil {
		log.Fatal("Error marshal json", err)
	}
	fmt.Fprintf(w, string(jsonByte))
}

func updateTimer(ticker int) {
	is.creds = getCredentials()
	t := time.NewTicker(time.Duration(ticker) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			elapsedSeconds := time.Now().Unix() - is.updatedUnixTime
			if updatableSeconds < elapsedSeconds {
				tmpCreds := getCredentials()
				if tmpCreds.Code == "Success" {
					mu.Lock()
					is.creds = tmpCreds
					is.updatedUnixTime = time.Now().Unix()
					mu.Unlock()
				}
			}
			break
		}
	}
}

func main() {
	if roleArn == "" {
		log.Fatalln("envvar[IMDS_ROLE_ARN] is not set")
	}
	if sourceProfile == "" {
		log.Println("envvar[IMDS_SOURCE_PROFILE] is not set, so use default")
		sourceProfile = "default"
	}
	roleSessionName = getIAMUsername()
	if roleSessionName == "" {
		roleSessionName = "jhondoe"
	}

	go updateTimer(checkInterval)

	http.HandleFunc("/", defaultHandler)
	http.HandleFunc(imdsCredentialPath, rolenameHandler)
	http.HandleFunc(imdsCredentialPath+iamRoleName, credentialHandler)
	http.ListenAndServe(":80", nil)
}
