#!/usr/bin/env python3
"""Render documentation assets from the TUI's own art and actual tmux captures.

Requires Pillow and a monospace font discoverable with fontconfig. Does not
change terminal text or use image generation. SVG art keeps each braille dot
as editable vector geometry. Run capture-dopamine-docs.py after UI changes.
"""
import json
from pathlib import Path
import re
import subprocess

from PIL import Image, ImageDraw, ImageFont

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'docs/assets/dopamine'
BG, FG, MUTED = '#171717', '#dededb', '#8a8a8a'
BITS = ((0, 0), (0, 1), (0, 2), (1, 0), (1, 1), (1, 2), (0, 3), (1, 3))
SGR = re.compile(r'\x1b\[([0-9;:]*)m')
ART = re.search(r'const Molecule = `([^`]+)`', (ROOT / 'internal/dopamine/model.go').read_text()).group(1)


def dot_positions(text, cell_width, cell_height):
    for row, line in enumerate(text.splitlines()):
        for col, char in enumerate(line):
            value = ord(char) - 0x2800
            if 0 <= value <= 255:
                for bit, (x, y) in enumerate(BITS):
                    if value & (1 << bit):
                        yield ((col + 0.28 + x * 0.44) * cell_width,
                               (row + 0.17 + y * 0.21) * cell_height)


def font(size, bold=False):
    pattern = 'monospace:style=Bold' if bold else 'monospace'
    path = subprocess.check_output(['fc-match', '-f', '%{file}', pattern], text=True)
    return ImageFont.truetype(path, size)


def art_svg():
    # The exact approved terminal artwork, as circles rather than font glyphs.
    circles = '\n'.join(f'<circle cx="{x:.2f}" cy="{y:.2f}" r="1.45"/>' for x, y in dot_positions(ART, 12, 24))
    width = max(map(len, ART.splitlines())) * 12
    (OUT / 'logo.svg').write_text(f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {width + 80} 248" role="img" aria-label="dopamine molecule">
<title>dopamine — the original terminal molecule</title>
<g transform="translate(40 40)" fill="#8a8a8a">{circles}</g>
</svg>
''')
    header = f'''<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="420" viewBox="0 0 1200 420" role="img" aria-label="dopamine — terminals, codex, projects">
<title>dopamine</title>
<rect width="1200" height="420" rx="18" fill="{BG}"/>
<text x="68" y="58" fill="{MUTED}" font-family="monospace" font-size="12" letter-spacing="4">TERMINAL WORKSPACES</text>
<g transform="translate(68 114) scale(1.1)" fill="{FG}">{circles}</g>
<text x="752" y="189" fill="{FG}" font-family="monospace" font-size="57" font-weight="bold" letter-spacing="-2">dopamine</text>
<text x="756" y="229" fill="{MUTED}" font-family="monospace" font-size="15">terminals / codex / projects</text>
<text x="756" y="259" fill="{MUTED}" font-family="monospace" font-size="15">less noise. more focus.</text>
<path d="M68 345H1132" stroke="#343434"/>
<g fill="{MUTED}" font-family="monospace" font-size="12" letter-spacing="2">
<text x="68" y="383">01  PROJECTS</text><text x="475" y="383">02  TERMINALS</text><text x="1010" y="383">03  CODEX</text>
</g>
</svg>
'''
    (OUT / 'header.svg').write_text(header)
    image = Image.new('RGB', (1200, 630), BG)
    draw = ImageDraw.Draw(image)
    draw.text((68, 55), 'TERMINAL WORKSPACES', font=font(17), fill=MUTED)
    for x, y in dot_positions(ART, 15, 30):
        draw.ellipse((68+x-1.8, 155+y-1.8, 68+x+1.8, 155+y+1.8), fill=FG)
    draw.text((68, 417), 'dopamine', font=font(64, True), fill=FG)
    draw.text((70, 510), 'terminals / codex / projects', font=font(23), fill=MUTED)
    image.save(OUT / 'social-preview.png', optimize=True)


def color(index):
    base = ['#171717', '#cc6666', '#a5b890', '#c5b58a', '#8aabc1', '#b19bb9', '#88b8b8', '#dededb',
            '#777777', '#df7d7d', '#b8cca3', '#d9c89b', '#a1bfd5', '#c8afcf', '#a1cccc', '#f0f0ed']
    if index < 16:
        return base[index]
    if index >= 232:
        value = 8 + (index - 232) * 10
        return (value, value, value)
    index -= 16
    ramp = [0, 95, 135, 175, 215, 255]
    return (ramp[index // 36], ramp[index // 6 % 6], ramp[index % 6])


def cells(line):
    fg, bg, reverse, bold, col = FG, BG, False, False, 0
    cursor = 0
    for match in list(SGR.finditer(line)) + [None]:
        end = match.start() if match else len(line)
        for char in line[cursor:end]:
            if char == '\t':
                col += 8 - col % 8
                continue
            yield col, char, bg if reverse else fg, fg if reverse else bg, bold
            col += 1
        if match is None:
            break
        values = [int(x or 0) for x in match.group(1).replace(':', ';').split(';')]
        i = 0
        while i < len(values):
            code = values[i]
            if code == 0:
                fg, bg, reverse, bold = FG, BG, False, False
            elif code == 1:
                bold = True
            elif code == 22:
                bold = False
            elif code in (7, 27):
                reverse = code == 7
            elif code == 39:
                fg = FG
            elif code == 49:
                bg = BG
            elif 30 <= code <= 37 or 90 <= code <= 97:
                fg = color(code - 30 if code < 90 else code - 90 + 8)
            elif 40 <= code <= 47 or 100 <= code <= 107:
                bg = color(code - 40 if code < 100 else code - 100 + 8)
            elif code in (38, 48) and i + 2 < len(values):
                if values[i + 1] == 5:
                    shade = color(values[i + 2])
                    i += 2
                elif values[i + 1] == 2 and i + 4 < len(values):
                    shade = tuple(values[i + 2:i + 5])
                    i += 4
                else:
                    i += 1
                    continue
                if code == 38:
                    fg = shade
                else:
                    bg = shade
            i += 1
        cursor = match.end()


def screenshot(name, panes, title):
    cell_w, cell_h, pad, top = 13, 27, 32, 73
    width, height = 100*cell_w + 2*pad, 28*cell_h + top + pad
    image = Image.new('RGB', (width, height), BG)
    draw = ImageDraw.Draw(image)
    regular, bold_font = font(21), font(21, True)
    draw.line((pad, 52, width-pad, 52), fill='#303030')
    draw.text((pad, 18), 'dopamine', font=font(16, True), fill=FG)
    draw.text((width-pad, 18), title, font=font(15), fill=MUTED, anchor='rt')
    for path, offset in panes:
        lines = path.read_text().splitlines()
        for row, line in enumerate(lines[:28]):
            for col, char, fg, bg, bold in cells(line):
                x, y = pad+(offset+col)*cell_w, top+row*cell_h
                if offset+col >= 100:
                    continue
                draw.rectangle((x, y, x+cell_w-1, y+cell_h-1), fill=bg)
                if 0x2800 <= ord(char) <= 0x28ff:
                    for dx, dy in dot_positions(char, cell_w, cell_h):
                        radius = 0.8
                        draw.ellipse((x+dx-radius, y+dy-radius, x+dx+radius, y+dy+radius), fill=fg)
                elif char != ' ':
                    draw.text((x, y+1), char, font=bold_font if bold else regular, fill=fg)
    image.save(OUT / (name + '.png'), optimize=True)


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    art_svg()
    source = OUT / 'captures'
    screenshot('dashboard', [(source / 'dashboard.txt', 0)], 'projects')
    screenshot('project-menu', [(source / 'project-menu.txt', 0)], 'project actions')
    layout = json.loads((source / 'layout.json').read_text())
    screenshot('terminal', [(source / 'terminal-rail.txt', 0),
                            (source / 'terminal-main.txt', layout['rail_columns'] + layout['separator_columns'])], 'native terminal')
    for name, title in [('cloud-working', 'cloud task · working'),
                        ('cloud-ready', 'cloud task · completed'),
                        ('autostart', 'server autostart')]:
        if (source / (name + '.txt')).exists():
            rail = source / (name + '-rail.txt')
            panes = [(rail, 0), (source / (name + '.txt'), 6)] if rail.exists() else [(source / (name + '.txt'), 0)]
            screenshot(name, panes, title)
    print('Rendered original molecule SVGs, social preview and three screenshots from real captures.')


if __name__ == '__main__':
    main()
