import copy
import json
from pathlib import Path
import xml.etree.ElementTree as ET
from .test_adapter import Base, make_profile
from raster_seating_adapter import extract_image, render_diagnostic
from raster_seating_adapter.cli import main

class EvidenceTests(Base):
    def test_evidence_and_xml_identity(self):
        m=extract_image(self.path,self.profile)
        obs=self.obs(m)
        m=extract_image(self.path,self.profile,obs)
        self.assertEqual(m['seats'][0]['evidence']['label_observation']['crop'],'crop-b')
        self.assertEqual(m['rows'][0]['label_evidence']['crop'],'crop-a')
        p=Path(self.d.name)/'diag.svg'; render_diagnostic(m,p)
        g=next(e for e in ET.parse(p).getroot().iter() if e.get('id')=='candidate-0001')
        self.assertEqual(g.get('data-row-label'),'X')
        self.assertEqual(g.get('data-seat-number'),'7')
        self.assertEqual(g.get('data-sellable'),'false')

    def test_empty_label_unhashable_ids_and_bad_rgb(self):
        m=extract_image(self.path,self.profile)
        for field,value in [('row_label',''),('geometric_band_id',[])]:
            o=self.obs(m); o['bands'][0][field]=value
            with self.assertRaises(ValueError): extract_image(self.path,self.profile,o)
        p=make_profile(blue_min=-1)
        with self.assertRaises(ValueError): extract_image(self.path,p)

    def test_low_confidence_keeps_evidence_without_number(self):
        m=extract_image(self.path,self.profile); o=self.obs(m)
        o['bands'][0]['seats'][0]['confidence']=0.2
        m=extract_image(self.path,self.profile,o)
        self.assertIsNone(m['seats'][0]['seat_number'])
        self.assertEqual(m['seats'][0]['evidence']['label_observation']['raw_number'],7)
        self.assertFalse(m['seats'][0]['sellable'])

    def test_cli_manifest_no_overwrite_and_corrupt_input(self):
        root=Path(self.d.name); profile=root/'profile.json'
        profile.write_text(json.dumps(self.profile)); out=root/'out'
        args=[self.path,'--profile',str(profile),'--output-dir',str(out)]
        self.assertEqual(main(args),2)
        before=(out/'seating.json').read_bytes()
        self.assertEqual(main(args),1)
        self.assertEqual((out/'seating.json').read_bytes(),before)
        bad=root/'bad.jpg'; bad.write_text('broken')
        self.assertEqual(main([str(bad),'--profile',str(profile),'--output-dir',str(root/'bad-out')]),1)
        self.assertEqual(json.loads((root/'bad-out/manifest.json').read_text())['status'],'INCOMPLETE')
        self.assertFalse((root/'bad-out/diagnostic.svg').exists())

    def test_thin_dispatch_never_invents_pass(self):
        from venue_generator.cli import main as dispatch
        self.assertEqual(dispatch(['unknown']),1)
        from unittest.mock import patch
        with patch('pdf_seating_adapter.cli.main',return_value=2) as run:
            self.assertEqual(dispatch(['pdf','source.pdf','--output-dir','new']),2)
            run.assert_called_once_with(['source.pdf','--output-dir','new'])
