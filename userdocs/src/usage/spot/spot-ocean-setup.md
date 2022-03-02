# Setup your environment

## Spot
Generate your credentials [here](https://console.spotinst.com/spt/settings/tokens/permanent). If you are not a Spot Ocean user, sign up for free [here](https://console.spotinst.com/spt/auth/signUp). For further information, please checkout our [Spot API](https://help.spot.io/spotinst-api/) guide, available on the [Spot Help Center](https://help.spot.io/) website.

To use environment variables, run:
```bash
export SPOTINST_TOKEN=<spotinst_token>
export SPOTINST_ACCOUNT=<spotinst_account>
```

To use credentials file, run the [spotctl configure](https://github.com/spotinst/spotctl#getting-started) command:
```bash
spotctl configure
? Enter your access token [? for help] **********************************
? Select your default account  [Use arrows to move, ? for more help]
> act-01234567 (prod)
  act-0abcdefg (dev)
```

Or, manually create an INI formatted file like this:
```ini
[default]
token   = <spotinst_token>
account = <spotinst_account>
```

and place it in:

- Unix/Linux/macOS:
```bash
~/.spotinst/credentials
```
- Windows:
```bash
%UserProfile%\.spotinst\credentials
```

## AWS

Make sure to set up your AWS credentials. Please refer to [Configuration and Credential File Settings](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-files.html) for further details.
