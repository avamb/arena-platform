// The build entry: the library and its stylesheet. Vite writes the stylesheet to
// dist/rich-editor.css (the host loads it, nothing is injected at run time).
// It is a separate file so that dist/types/index.d.ts has no CSS import.
import './rich-editor.css'

export * from './index'
