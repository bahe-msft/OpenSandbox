// Copyright 2026 Alibaba Group Holding Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
	"unicode/utf8"

	credentialproviderv1 "github.com/alibaba/opensandbox/egress/pkg/credentialprovider/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultTokenFile = "/var/run/secrets/opensandbox/serviceaccount/token"
	maxMessageBytes  = 64 << 10
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("opensandbox-psat", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tokenFile := flags.String("token-file", defaultTokenFile, "projected ServiceAccount token file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *tokenFile == "" {
		return writeResponse(stdout, credentialproviderv1.CredentialResponse_UNAVAILABLE, nil, time.Time{}, "invalid_configuration")
	}

	requestData, err := readLimited(stdin, maxMessageBytes)
	if err != nil {
		return writeResponse(stdout, credentialproviderv1.CredentialResponse_UNAVAILABLE, nil, time.Time{}, "invalid_request")
	}
	var request credentialproviderv1.CredentialRequest
	if err := proto.Unmarshal(requestData, &request); err != nil ||
		request.ApiVersion != credentialproviderv1.APIVersion || request.PluginName != "opensandbox-psat" {
		return writeResponse(stdout, credentialproviderv1.CredentialResponse_DENIED, nil, time.Time{}, "invalid_request")
	}

	token, expiresAt, err := readToken(*tokenFile)
	if err != nil {
		return writeResponse(stdout, credentialproviderv1.CredentialResponse_UNAVAILABLE, nil, time.Time{}, "token_unavailable")
	}
	return writeResponse(stdout, credentialproviderv1.CredentialResponse_SUCCESS, token, expiresAt, "")
}

func readToken(path string) ([]byte, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("open token")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, time.Time{}, fmt.Errorf("invalid token file")
	}
	data, err := readLimited(file, maxMessageBytes)
	if err != nil || len(data) == 0 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 || bytes.ContainsAny(data, "\r\n") {
		return nil, time.Time{}, fmt.Errorf("invalid token")
	}
	expiresAt, valid := validateJWT(data, time.Now())
	if !valid {
		return nil, time.Time{}, fmt.Errorf("invalid token")
	}
	return data, expiresAt, nil
}

func validateJWT(token []byte, now time.Time) (time.Time, bool) {
	parts := bytes.Split(token, []byte("."))
	if len(parts) != 3 || len(parts[2]) == 0 {
		return time.Time{}, false
	}
	for _, part := range parts[:2] {
		decoded, err := base64.RawURLEncoding.DecodeString(string(part))
		if err != nil || !json.Valid(decoded) {
			return time.Time{}, false
		}
	}
	payload, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
		NotBefore int64 `json:"nbf"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.ExpiresAt <= 0 {
		return time.Time{}, false
	}
	expiresAt := time.Unix(claims.ExpiresAt, 0)
	if !expiresAt.After(now) || (claims.NotBefore > 0 && time.Unix(claims.NotBefore, 0).After(now.Add(30*time.Second))) {
		return time.Time{}, false
	}
	return expiresAt, true
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) == 0 || int64(len(data)) > limit {
		return nil, fmt.Errorf("invalid bounded input")
	}
	return data, nil
}

func writeResponse(stdout io.Writer, status credentialproviderv1.CredentialResponse_Status, credential []byte, expiresAt time.Time, errorCode string) error {
	response := &credentialproviderv1.CredentialResponse{
		ApiVersion: credentialproviderv1.APIVersion,
		Status:     status,
		Credential: credential,
		Encoding:   credentialproviderv1.CredentialResponse_UTF8,
		Cacheable:  false,
		ErrorCode:  errorCode,
	}
	if !expiresAt.IsZero() {
		response.ExpiresAt = timestamppb.New(expiresAt)
	}
	data, err := proto.Marshal(response)
	if err != nil {
		return err
	}
	_, err = stdout.Write(data)
	return err
}
