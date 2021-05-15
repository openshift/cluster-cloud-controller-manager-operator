package render

// Render defines render config for use in bootstrap mode
type Render struct {
}

// New returns controller for render
func New() *Render {
	return &Render{}
}

// Run runs boostrap for Machine Config Controller
// It writes all the assets to destDir
func (b *Render) Run(destDir string) error {
	return nil
}
