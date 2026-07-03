module orq-rc

go 1.25.0

// The rc CLI shares the root module's hand-written custom commands
// (cli/custom) and only swaps in its own generated command set.
replace orq => ../..

require (
	github.com/orq-ai/bartolo v0.4.3
	github.com/pkg/errors v0.8.1
	github.com/rs/zerolog v1.11.0
	github.com/spf13/cobra v1.10.2
	github.com/spf13/viper v1.2.1
	gopkg.in/h2non/gentleman.v2 v2.0.3
	orq v0.0.0
)

require (
	github.com/AlecAivazis/survey/v2 v2.3.7 // indirect
	github.com/alecthomas/chroma v0.0.0-20181013211843-01e18834b5dd // indirect
	github.com/danwakefield/fnmatch v0.0.0-20160403171240-cbb64ac3d964 // indirect
	github.com/dlclark/regexp2 v1.1.6 // indirect
	github.com/fsnotify/fsnotify v1.4.7 // indirect
	github.com/hashicorp/hcl v1.0.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/magiconair/properties v1.8.0 // indirect
	github.com/mattn/go-colorable v0.1.2 // indirect
	github.com/mattn/go-isatty v0.0.8 // indirect
	github.com/mattn/go-runewidth v0.0.3 // indirect
	github.com/mgutz/ansi v0.0.0-20170206155736-9520e82c474b // indirect
	github.com/mitchellh/mapstructure v1.1.2 // indirect
	github.com/olekukonko/tablewriter v0.0.0-20180912035003-be2c049b30cc // indirect
	github.com/pelletier/go-toml v1.2.0 // indirect
	github.com/spf13/afero v1.1.2 // indirect
	github.com/spf13/cast v1.2.0 // indirect
	github.com/spf13/jwalterweatherman v1.0.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/toon-format/toon-go v0.0.0-20251202084852-7ca0e27c4e8c // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/term v0.42.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	gopkg.in/yaml.v2 v2.2.8 // indirect
)
