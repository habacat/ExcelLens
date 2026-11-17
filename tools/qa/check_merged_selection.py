"""Compare actual painted pixels inside vertical merged header anchors."""
import json
import math
import sys
from pathlib import Path
from PIL import Image, ImageChops

directory = Path(sys.argv[1]).resolve()
manifest = json.loads((directory / 'scenarios.json').read_text(encoding='utf-8'))
checks = []
for scenario in manifest['scenarios']:
    images = [Image.open(directory / name).convert('RGB') for name in scenario['files']]
    assert all(image.size == images[0].size for image in images), 'paint geometry changed during selection'
    scale_x = images[0].width / scenario['clip']['width']
    scale_y = images[0].height / scenario['clip']['height']
    for box in scenario['boxes']:
        # Exclude shared outside borders and adjacent row highlighting.
        crop = (math.ceil((box['x'] - scenario['clip']['x'] + 1) * scale_x),
                math.ceil((box['y'] - scenario['clip']['y'] + 1) * scale_y),
                math.floor((box['x'] - scenario['clip']['x'] + box['width'] - 1) * scale_x),
                math.floor((box['y'] - scenario['clip']['y'] + box['height'] - 1) * scale_y))
        crops = [image.crop(crop) for image in images]
        counts = [sum(1 for pixel in ImageChops.difference(crops[0], image).getdata() if max(pixel) > 1)
                  for image in crops[1:]]
        checks.append({'mode': scenario['mode'], 'zoom': scenario['zoom'], 'dpr': scenario['dpr'],
                       'ref': box['ref'], 'coveredRowChangedPixels': counts[0], 'returnChangedPixels': counts[1]})

changed = sum(item['coveredRowChangedPixels'] for item in checks)
returned = sum(item['returnChangedPixels'] for item in checks)
reproduction = '--expect-occlusion' in sys.argv
passed = (changed > 0 and returned == 0) if reproduction else (changed == 0 and returned == 0)
report = {'result': 'PASS' if passed else 'FAIL', 'purpose': 'reproduce original defect' if reproduction else 'merged header stays painted during selection',
          'scenarios': len(manifest['scenarios']), 'anchorsChecked': len(checks), 'changedPixels': changed,
          'returnChangedPixels': returned, 'sourceSHA256': manifest['sourceSHA256'], 'sourceUnchanged': manifest['sourceUnchanged'], 'checks': checks}
(directory / 'pixel-results.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
print(json.dumps({key: value for key, value in report.items() if key != 'checks'}, ensure_ascii=False))
if not passed:
    raise SystemExit(1)
