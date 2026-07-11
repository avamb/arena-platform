(function() {
  var el = wp.element.createElement;
  var __ = wp.i18n.__;

  wp.blocks.registerBlockType('arena-events/arena-tickets', {
    edit: function(props) {
      var attrs = props.attributes;
      var setAttributes = props.setAttributes;
      var InspectorControls = wp.blockEditor.InspectorControls;
      var PanelBody = wp.components.PanelBody;
      var TextControl = wp.components.TextControl;
      var SelectControl = wp.components.SelectControl;

      return [
        el(InspectorControls, { key: 'inspector' },
          el(PanelBody, { title: __('Widget Settings', 'arena-events'), initialOpen: true },
            el(TextControl, {
              label: __('Feed Token', 'arena-events'),
              help: __('Required. Feed token from Arena platform.', 'arena-events'),
              value: attrs.feedToken,
              onChange: function(v) { setAttributes({ feedToken: v }); },
            }),
            el(TextControl, {
              label: __('Session ID', 'arena-events'),
              help: __('Optional. Pre-select a specific event session.', 'arena-events'),
              value: attrs.sessionId,
              onChange: function(v) { setAttributes({ sessionId: v }); },
            }),
            el(SelectControl, {
              label: __('Locale', 'arena-events'),
              value: attrs.locale,
              options: [
                { label: 'English', value: 'en' },
                { label: 'Русский', value: 'ru' },
                { label: 'Čeština', value: 'cs' },
                { label: 'עברית', value: 'he' },
              ],
              onChange: function(v) { setAttributes({ locale: v }); },
            }),
            el(TextControl, {
              label: __('CDN Base URL', 'arena-events'),
              help: __('Leave empty to use the default CDN.', 'arena-events'),
              value: attrs.cdnBase,
              onChange: function(v) { setAttributes({ cdnBase: v }); },
            })
          )
        ),
        el('div', { key: 'preview', style: { padding: '1rem', background: '#f0f0f0', borderRadius: '4px', textAlign: 'center' } },
          el('strong', {}, 'Arena Tickets Widget'),
          attrs.feedToken
            ? el('p', { style: { margin: '0.5rem 0 0', fontSize: '12px', color: '#555' } },
                'Feed Token: ' + attrs.feedToken + (attrs.sessionId ? ' | Session: ' + attrs.sessionId : '')
              )
            : el('p', { style: { margin: '0.5rem 0 0', fontSize: '12px', color: '#c00' } },
                'Please enter a Feed Token in the block settings.')
        ),
      ];
    },
    save: function() {
      // Server-side rendered — return null so WordPress calls render_callback.
      return null;
    },
  });
})();
