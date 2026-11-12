import asyncio, importlib.util, os, sys, tempfile, unittest
HERE=os.path.dirname(os.path.abspath(__file__))
def load(name):
    spec=importlib.util.spec_from_file_location(name+"_mod", os.path.join(HERE,name+".py"))
    m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m); return m
IP1="ip -4 -o address show dev ens18 scope global\n"
def out(*rows):
    return "\n".join(f"2: ens18    inet {ip}/24 brd x scope global {'secondary ' if sec else ''}ens18" for ip,sec in rows)
PRIMARY=("10.0.0.5",False)
for MOD in ("relay",):
    m=load(MOD)
    class T(unittest.TestCase):
        mod=m
        def R(self, pin="", pref="", state=""):
            return self.mod.OutboundSourceResolver(pin,"ens18",60,pref,state)
        def run_resolve(self, r, outputs, force=True):
            async def go():
                seq=iter(outputs)
                async def fake(): return r.parse_addresses(next(seq))
                r._list_addresses=fake
                res=[]
                for _ in outputs:
                    try: res.append(await r.resolve(force=force))
                    except RuntimeError as e: res.append(e)
                return res
            return asyncio.run(go())
        def test_parse(self):
            g,s=self.mod.OutboundSourceResolver.parse_addresses(out(PRIMARY,("1.1.1.1",True)))
            self.assertEqual(g,["10.0.0.5","1.1.1.1"]); self.assertEqual(s,["1.1.1.1"])
        def test_single_secondary_legacy(self):
            self.assertEqual(self.run_resolve(self.R(),[out(PRIMARY,("1.1.1.1",True))]),["1.1.1.1"])
        def test_pin_highest_priority(self):
            r=self.R(pin="9.9.9.9")
            async def boom(): raise AssertionError("no discovery with pin")
            r._list_addresses=boom
            self.assertEqual(asyncio.run(r.resolve(force=True)),"9.9.9.9")
        def test_sticky_when_new_ips_appear(self):
            res=self.run_resolve(self.R(),[out(PRIMARY,("1.1.1.1",True)),
                out(PRIMARY,("1.1.1.1",True),("2.2.2.2",True),("3.3.3.3",True))])
            self.assertEqual(res,["1.1.1.1","1.1.1.1"])
        def test_sticky_ip_disappears_fail_closed(self):
            res=self.run_resolve(self.R(),[out(PRIMARY,("1.1.1.1",True)),out(PRIMARY,("2.2.2.2",True))])
            self.assertEqual(res[0],"1.1.1.1"); self.assertIsInstance(res[1],RuntimeError)
            self.assertIn("refusing to switch",str(res[1]))
        def test_multi_at_start_no_hint_fails_closed(self):
            res=self.run_resolve(self.R(),[out(PRIMARY,("1.1.1.1",True),("2.2.2.2",True))])
            self.assertIsInstance(res[0],RuntimeError); self.assertIn("found 2",str(res[0]))
        def test_multi_at_start_preferred(self):
            res=self.run_resolve(self.R(pref="2.2.2.2"),[out(PRIMARY,("1.1.1.1",True),("2.2.2.2",True))])
            self.assertEqual(res,["2.2.2.2"])
        def test_preferred_absent_fails(self):
            res=self.run_resolve(self.R(pref="7.7.7.7"),[out(PRIMARY,("1.1.1.1",True),("2.2.2.2",True))])
            self.assertIsInstance(res[0],RuntimeError)
        def test_state_file_persist_and_reuse(self):
            with tempfile.TemporaryDirectory() as d:
                f=os.path.join(d,"src")
                self.assertEqual(self.run_resolve(self.R(state=f),[out(PRIMARY,("1.1.1.1",True))]),["1.1.1.1"])
                self.assertEqual(open(f).read().strip(),"1.1.1.1")
                res=self.run_resolve(self.R(state=f),[out(PRIMARY,("2.2.2.2",True),("1.1.1.1",True))])
                self.assertEqual(res,["1.1.1.1"])
        def test_state_ip_gone_falls_to_rules(self):
            with tempfile.TemporaryDirectory() as d:
                f=os.path.join(d,"src"); open(f,"w").write("5.5.5.5\n")
                res=self.run_resolve(self.R(state=f),[out(PRIMARY,("1.1.1.1",True),("2.2.2.2",True))])
                self.assertIsInstance(res[0],RuntimeError)
        def test_recovery_after_fail_requires_same_ip(self):
            res=self.run_resolve(self.R(),[out(PRIMARY,("1.1.1.1",True)),out(PRIMARY,("2.2.2.2",True)),out(PRIMARY,("1.1.1.1",True),("2.2.2.2",True))])
            self.assertEqual(res[2],"1.1.1.1")
    T.__name__=f"Test_{MOD}"; globals()[T.__name__]=T; del T
