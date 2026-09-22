// Package cobra is a stub of the command surface.
package cobra

type Command struct {
	Use  string
	Run  func(cmd *Command, args []string)
	RunE func(cmd *Command, args []string) error
	subs []*Command
}

func (c *Command) AddCommand(cmds ...*Command) { c.subs = append(c.subs, cmds...) }
func (c *Command) Execute() error {
	if c.RunE != nil {
		return c.RunE(c, nil)
	}
	if c.Run != nil {
		c.Run(c, nil)
	}
	return nil
}
