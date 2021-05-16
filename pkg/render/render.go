package render

// Render defines render config for use in bootstrap mode
type Render struct {
	// path to rendered configv1.Infrastructure manifest
	infrastructureFile string
	// path to rendered cloud-controller-manager-images ConfigMap manifest for image references to use
	imagesFile string
}

// New returns controller for render
func New(infrastructureFile, imagesFile string) *Render {
	return &Render{
		infrastructureFile: infrastructureFile,
		imagesFile:         imagesFile,
	}
}

// Run runs boostrap for Machine Config Controller
// It writes all the assets to destDir
func (b *Render) Run(destinationDir string) error {
	return nil
}
