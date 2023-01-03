package app

import (
	"fmt"
	"github.com/apex/log"
	"github.com/go-chi/jwtauth/v5"
	"github.com/urfave/cli/v2"
	"os"
)

func newDebugCmd() *cli.Command {
	return &cli.Command{
		Name:  "debug",
		Usage: "Get authorization information for debugging",
		Action: func(c *cli.Context) error {
			conf, err := loadConfig(c)
			if err != nil {
				return err
			}

			tokenAuth := jwtauth.New("HS256", []byte(conf.JWTSecret), nil)

			claims := make(map[string]interface{})
			jwtauth.SetIssuedNow(claims)
			_, tokenString, _ := tokenAuth.Encode(claims)
			if os.Getenv("MINIMAL_OUTPUT") == "1" {
				// for scripting
				fmt.Printf("Bearer %s", tokenString)
			} else {
				log.Infof("Send the following HTTP header for JWT authorization:\n    Authorization: Bearer %s", tokenString)
			}

			return nil
		},
	}
}
