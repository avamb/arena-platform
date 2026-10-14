# seating_svg_renderer

Минимальный детерминированный renderer для `seating-json-v1`. Модуль принимает
только source-only JSON и не знает о XLSX-профилях, эталонных SVG, Bil24 или
Arena.

Координаты берутся из `computed_geometry.grid_x/grid_y` (с резервом на
`source_coordinate.column/row`) и переносятся в SVG единицы тождественно:
`svg_x = grid_x`, `svg_y = grid_y`. Это диагностическая сетка, а не физическая
калибровка. `PASS`/`VALID` допускаются только при явных номерах и координатах.
`QUARANTINE` по умолчанию отклоняется; флаг диагностического режима рисует
неподтверждённые позиции только прямоугольными маркерами, никогда не кругами.

## CLI

```powershell
python -m seating_svg_renderer render --input xlsx_seating_adapter/results/hunter.json --output seating_svg_renderer/results/hunter.svg
python -m seating_svg_renderer render --input xlsx_seating_adapter/results/aribau.json --output seating_svg_renderer/results/aribau.svg
python -m seating_svg_renderer validate --model xlsx_seating_adapter/results/hunter.json --svg seating_svg_renderer/results/hunter.svg
```
