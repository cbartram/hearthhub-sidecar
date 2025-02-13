package cmd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
)

type CognitoService interface {
	GetUserAttributes(ctx context.Context, accessToken *string) ([]types.AttributeType, error)
	AuthUser(ctx context.Context, refreshToken, discordId *string) (*CognitoUser, error)
	UpdateUserAttributes(ctx context.Context, accessToken *string, attributes []types.AttributeType) error
	MergeInstalledFilesBatch(ctx context.Context, user *CognitoUser, fileNames []string) error
}

type CognitoServiceImpl struct {
	cognitoClient *cognitoidentityprovider.Client
	userPoolID    string
	clientID      string
	clientSecret  string
	configPath    string
}

type CognitoCredentials struct {
	RefreshToken    string `json:"refresh_token,omitempty"`
	TokenExpiration int32  `json:"token_expiration_seconds,omitempty"`
	AccessToken     string `json:"access_token,omitempty"`
	IdToken         string `json:"id_token,omitempty"`
}

type CognitoUser struct {
	CognitoID       string             `json:"cognitoId,omitempty"`
	DiscordUsername string             `json:"discordUsername,omitempty"`
	Email           string             `json:"email,omitempty"`
	DiscordID       string             `json:"discordId,omitempty"`
	AccountEnabled  bool               `json:"accountEnabled,omitempty"`
	Credentials     CognitoCredentials `json:"credentials,omitempty"`
}

// SessionData represents locally stored session information
type SessionData struct {
	RefreshToken string `json:"refresh_token"`
}

type InstalledFile struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
}

// MakeCognitoService creates a new instance of CognitoAuthManager
func MakeCognitoService(awsConfig aws.Config) CognitoService {
	return &CognitoServiceImpl{
		cognitoClient: cognitoidentityprovider.NewFromConfig(awsConfig),
		userPoolID:    os.Getenv("USER_POOL_ID"),
		clientID:      os.Getenv("COGNITO_CLIENT_ID"),
		clientSecret:  os.Getenv("COGNITO_CLIENT_SECRET"),
		configPath:    filepath.Join(os.Getenv("HOME"), ".config", "hearthhub-file-install", "session.json"),
	}
}

// MakeCognitoSecretHash Creates a hash based on the user id, service id and secret which must be
// sent with every cognito auth request (along with a refresh token) to get a new access token.
func MakeCognitoSecretHash(userId, clientId, clientSecret string) string {
	usernameClientID := userId + clientId
	hash := hmac.New(sha256.New, []byte(clientSecret))
	hash.Write([]byte(usernameClientID))
	digest := hash.Sum(nil)

	return base64.StdEncoding.EncodeToString(digest)
}

// MergeInstalledFilesBatch Updates a users attribute called: custom:installed_files by merging the existing
// state with any new backup files that were just installed on the PVC.
func (c *CognitoServiceImpl) MergeInstalledFilesBatch(ctx context.Context, user *CognitoUser, fileNames []string) error {
	installedFiles := make(map[string]bool)
	attributes, err := c.GetUserAttributes(ctx, &user.Credentials.AccessToken)
	if err != nil {
		log.Errorf("failed to get user attributes: %v", err)
		return err
	}

	for _, attribute := range attributes {
		if *attribute.Name == "custom:installed_backups" {
			// Deserialize the json string value of the attribute into a struct
			err := json.Unmarshal([]byte(*attribute.Value), &installedFiles)
			if err != nil {
				log.Errorf("failed to unmarshal installed mods: %v", err)
				return err
			}
			break
		}
	}

	log.Infof("files before: %v", installedFiles)

	// for each .db file, insert or update the user's installed_backups attribute with
	// a true value for the file since it was found on the pvc. "fileNames" is the list of backup files directly from the
	// pvc.
	for _, fileName := range fileNames {
		log.Infof("merging file: %s", fileName)
		installedFiles[fileName] = true
	}

	// Serialize the installed mods to json
	mergedBytes, err := json.Marshal(installedFiles)
	if err != nil {
		log.Errorf("failed to marshal installed files: %v", err)
		return err
	}

	attr := types.AttributeType{
		Name:  aws.String("custom:installed_backups"),
		Value: aws.String(string(mergedBytes)),
	}

	log.Infof("merged files after: %v", installedFiles)
	err = c.UpdateUserAttributes(ctx, &user.Credentials.AccessToken, []types.AttributeType{attr})
	if err != nil {
		log.Errorf("failed to update user attributes: %v", err)
		return err
	}

	return nil
}

func (c *CognitoServiceImpl) GetUserAttributes(ctx context.Context, accessToken *string) ([]types.AttributeType, error) {
	user, err := c.cognitoClient.GetUser(ctx, &cognitoidentityprovider.GetUserInput{AccessToken: accessToken})

	if err != nil {
		log.Errorf("could not get user with access token: %v", err)
		return nil, errors.New("could not get user with access token")
	}

	return user.UserAttributes, nil
}

func (c *CognitoServiceImpl) UpdateUserAttributes(ctx context.Context, accessToken *string, attributes []types.AttributeType) error {
	_, err := c.cognitoClient.UpdateUserAttributes(ctx, &cognitoidentityprovider.UpdateUserAttributesInput{
		AccessToken:    accessToken,
		UserAttributes: attributes,
	})

	if err != nil {
		log.Errorf("could not update user attributes with access token: %v", err)
		return errors.New(fmt.Sprintf("could not update user attributes with access token: %v", err))
	}

	return nil
}

func (c *CognitoServiceImpl) AuthUser(ctx context.Context, refreshToken, discordId *string) (*CognitoUser, error) {
	auth, err := c.cognitoClient.AdminInitiateAuth(ctx, &cognitoidentityprovider.AdminInitiateAuthInput{
		UserPoolId: aws.String(c.userPoolID),
		ClientId:   aws.String(c.clientID),
		AuthFlow:   types.AuthFlowTypeRefreshTokenAuth,
		AuthParameters: map[string]string{
			"REFRESH_TOKEN": *refreshToken,
			"SECRET_HASH":   MakeCognitoSecretHash(*discordId, c.clientID, c.clientSecret),
		},
	})

	if err != nil {
		log.Errorf("error auth: user %s could not be authenticated: %s", *discordId, err)
		return nil, err
	}

	user, err := c.cognitoClient.AdminGetUser(ctx, &cognitoidentityprovider.AdminGetUserInput{
		UserPoolId: aws.String(c.userPoolID),
		Username:   discordId,
	})

	if err != nil {
		log.Errorf("could not get user with username: %s: error: %s", *discordId, err.Error())
		return nil, err
	}

	var email, discordID, discordUsername, cognitoID string
	for _, attr := range user.UserAttributes {
		switch aws.ToString(attr.Name) {
		case "email":
			email = aws.ToString(attr.Value)
		case "sub":
			cognitoID = aws.ToString(attr.Value)
		case "custom:discord_id":
			discordID = aws.ToString(attr.Value)
		case "custom:discord_username":
			discordUsername = aws.ToString(attr.Value)
		}
	}

	// Note: we still authenticate a disabled user the service side handles updating UI/auth flows
	// to re-auth with discord.
	return &CognitoUser{
		DiscordUsername: discordUsername,
		DiscordID:       discordID,
		Email:           email,
		CognitoID:       cognitoID,
		AccountEnabled:  user.Enabled,
		Credentials: CognitoCredentials{
			AccessToken:     *auth.AuthenticationResult.AccessToken,
			RefreshToken:    *refreshToken,
			TokenExpiration: auth.AuthenticationResult.ExpiresIn,
			IdToken:         *auth.AuthenticationResult.IdToken,
		},
	}, nil
}
