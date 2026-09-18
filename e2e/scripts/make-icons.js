#!/usr/bin/env node
// @ts-check
'use strict';
/*
 * Renders the app icons from the one drawing in web/src/favicon.svg.
 *
 *   cd e2e && node scripts/make-icons.js
 *
 * A phone's home screen does not take an SVG: iOS wants a 180px PNG named by
 * <link rel="apple-touch-icon">, and Android will not offer "install" without
 * 192 and 512px PNGs in the manifest. The maskable one is drawn full-bleed with
 * the glass inside the safe zone - the inner 80% - because Android crops it to
 * whatever shape the launcher likes, and an icon with its own rounded corners
 * inside a circle looks like a mistake.
 *
 * Chromium does the drawing, since it is already here for the browser tests
 * and nothing else in this repository can rasterise an SVG.
 */
const fs = require('fs');
const path = require('path');
const { chromium } = require('@playwright/test');

const src = path.join(__dirname, '..', '..', 'web', 'src');
const svg = fs.readFileSync(path.join(src, 'favicon.svg'), 'utf8');
const inner = svg.replace(/^[\s\S]*?<svg[^>]*>/, '').replace(/<\/svg>\s*$/, '');
const glyph = inner.replace(/<rect[^>]*\/>/, '');          // everything but the rounded background
const DUSK = '#231B3A';

const rounded = (size) => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" width="${size}" height="${size}">${inner}</svg>`;
// Full-bleed square; the drawing scaled to 72% about the centre, inside the safe zone.
const bleed = (size) => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" width="${size}" height="${size}">
  <rect width="32" height="32" fill="${DUSK}"/><g transform="translate(16 16) scale(0.72) translate(-16 -16)">${glyph}</g></svg>`;

const icons = [
  ['icon-192.png', 192, rounded],
  ['icon-512.png', 512, rounded],
  ['icon-maskable-512.png', 512, bleed],
  ['apple-touch-icon.png', 180, bleed],      // iOS rounds the corners itself
];

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ deviceScaleFactor: 1 });
  for (const [name, size, draw] of icons) {
    await page.setViewportSize({ width: size, height: size });
    await page.setContent(`<style>html,body{margin:0;background:transparent}svg{display:block}</style>${draw(size)}`);
    await page.screenshot({ path: path.join(src, 'icons', name), omitBackground: true, clip: { x: 0, y: 0, width: size, height: size } });
    console.log(name, fs.statSync(path.join(src, 'icons', name)).size, 'bytes');
  }
  await browser.close();
})();
