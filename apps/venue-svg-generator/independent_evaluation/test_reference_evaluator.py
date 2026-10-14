"""Regression tests for nested SVG transforms and explicit alignment."""
from pathlib import Path
import ast
import unittest
import numpy as np
from evaluate_reference import matrix, fit_similarity


class EvaluationTests(unittest.TestCase):
    def test_transform_order_and_mirror(self):
        point=matrix('translate(20,30) scale(-1,1)') @ [4,5,1]
        np.testing.assert_allclose(point,[16,35,1])

    def test_rotation_about_center(self):
        np.testing.assert_allclose(matrix('rotate(90,10,20)') @ [12,20,1],[10,22,1],atol=1e-12)

    def test_matrix_and_unsupported_transform(self):
        np.testing.assert_allclose(matrix('matrix(2,0,0,-2,3,9)') @ [4,5,1],[11,-1,1])
        with self.assertRaises(ValueError):matrix('skewX(15)')

    def test_similarity_can_report_reversed_orientation(self):
        source=np.array([[0,0],[1,0],[0,2],[1,2]])
        target=source @ np.array([[-3,0],[0,3]]) + [10,20]
        t=fit_similarity(source,target)
        predicted=(t @ np.column_stack((source,np.ones(len(source)))).T).T[:,:2]
        np.testing.assert_allclose(predicted,target,atol=1e-12)
        self.assertLess(np.linalg.det(t[:2,:2]),0)

    def test_generator_does_not_import_evaluator(self):
        root=Path(__file__).resolve().parents[1]
        for filename in ('build_seating_model.py','generate_prototype.py','verify_prototype.py'):
            path=root/'seating_poc'/filename
            tree=ast.parse(path.read_text(encoding='utf-8'))
            imports=[n.module for n in ast.walk(tree) if isinstance(n,ast.ImportFrom)]
            imports += [a.name for n in ast.walk(tree) if isinstance(n,ast.Import) for a in n.names]
            self.assertFalse(any('evaluat' in (name or '') for name in imports))
            self.assertNotIn('Palac_Akropolis.svg',path.read_text(encoding='utf-8'))


if __name__=='__main__':unittest.main(verbosity=2)
