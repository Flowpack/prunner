package app

import (
	"github.com/apex/log"
	"github.com/go-chi/jwtauth/v5"
	"github.com/urfave/cli/v2"

	"github.com/Flowpack/prunner/config"
)

func newDebugCmd() *cli.Command {
	return &cli.Command{
		Name:  "debug",
		Usage: "Get authorization information for debugging",
		Action: func(c *cli.Context) error {
			conf, err := config.LoadOrCreateConfig(c.String("config"))
			if err != nil {
				return err
			}

			tokenAuth := jwtauth.New("HS256", []byte(conf.JWTSecret), nil)

			claims := make(map[string]interface{})
			jwtauth.SetIssuedNow(claims)
			_, tokenString, _ := tokenAuth.Encode(claims)
			log.Infof("Send the following HTTP header for JWT authorization:\n    Authorization: Bearer %s", tokenString)

			return nil
		},
	}
}
