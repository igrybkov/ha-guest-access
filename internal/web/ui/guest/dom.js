// Small DOM helpers. Text goes in with textContent only, so no server
// value is ever parsed as markup.

const SVG = 'http://www.w3.org/2000/svg';

// el makes an element. props sets properties (className, type, disabled,
// and so on) except these keys:
//   attrs: attributes to set, such as aria-label;
//   on: event handlers by event name.
// Children are nodes or strings; strings become text nodes.
export function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  const { attrs, on, ...rest } = props;
  for (const [k, v] of Object.entries(rest)) {
    if (v !== undefined && v !== null) node[k] = v;
  }
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v !== undefined && v !== null && v !== false) node.setAttribute(k, String(v));
  }
  for (const [k, fn] of Object.entries(on || {})) node.addEventListener(k, fn);
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    node.append(typeof c === 'string' ? document.createTextNode(c) : c);
  }
  return node;
}

// Icon paths, 24x24, drawn with currentColor.
const PATHS = {
  play: 'M8 5v14l11-7z',
  pause: 'M6 5h4v14H6zM14 5h4v14h-4z',
  next: 'M6 18l8.5-6L6 6v12zM16 6h2v12h-2z',
  prev: 'M6 6h2v12H6zm3.5 6l8.5 6V6z',
  volume: 'M3 9v6h4l5 5V4L7 9H3zm13.5 3A4.5 4.5 0 0 0 14 7.97v8.05A4.5 4.5 0 0 0 16.5 12z',
  muted: 'M3 9v6h4l5 5V4L7 9H3zm13.6 3 2.7-2.7-1.4-1.4-2.7 2.7-2.7-2.7-1.4 1.4 2.7 2.7-2.7 2.7 1.4 1.4 2.7-2.7 2.7 2.7 1.4-1.4z',
  pin: 'M12 2a7 7 0 0 0-7 7c0 5.25 7 13 7 13s7-7.75 7-13a7 7 0 0 0-7-7zm0 9.5A2.5 2.5 0 1 1 12 6.5a2.5 2.5 0 0 1 0 5z',
  music: 'M12 3v10.55A4 4 0 1 0 14 17V7h4V3h-6z',
  check: 'M9 16.2 4.8 12l-1.4 1.4L9 19 21 7l-1.4-1.4z',
};

// icon makes an inline SVG icon. It is hidden from screen readers; the
// button around it carries the label.
export function icon(name) {
  const svg = document.createElementNS(SVG, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  svg.setAttribute('class', 'icon');
  const p = document.createElementNS(SVG, 'path');
  p.setAttribute('d', PATHS[name] || '');
  p.setAttribute('fill', 'currentColor');
  svg.append(p);
  return svg;
}
