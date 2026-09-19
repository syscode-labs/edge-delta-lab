#!/usr/bin/env python3
"""Generate editable native Excalidraw scenes and matching lightweight SVG previews.

No remote API, image generation, embedded fonts, or npm dependency. The SVG is a
preview rendered from the same primitives, not an Excalidraw browser screenshot.
"""
from __future__ import annotations
import html
import json
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]
OUT=ROOT/'docs/diagrams'

class Scene:
    def __init__(self,title,subtitle,w=1560,h=1060):
        self.elements=[];self.svg=[];self.w=w;self.h=h
        self.text(45,35,title,34,'#172b4d')
        self.text(45,88,subtitle,19,'#526578')
    def element(self,kind,x,y,w,h,**extra):
        i=len(self.elements)+1
        item=dict(id=f'e{i}',type=kind,x=x,y=y,width=w,height=h,angle=0,
                  strokeColor='#243b53',backgroundColor='transparent',fillStyle='solid',
                  strokeWidth=2,strokeStyle='solid',roughness=0.7,opacity=100,groupIds=[],frameId=None,
                  roundness=None,seed=i*1729,version=1,versionNonce=i*19,isDeleted=False,
                  boundElements=None,updated=1,link=None,locked=False)
        item.update(extra);self.elements.append(item);return item
    def text(self,x,y,text,size=20,color='#243b53',group=None):
        lines=text.split('\n');w=max(len(line) for line in lines)*size*.59;h=len(lines)*size*1.3
        e=self.element('text',x,y,w,h,text=text,originalText=text,fontSize=size,fontFamily=2,
                       textAlign='left',verticalAlign='top',containerId=None,autoResize=True,
                       lineHeight=1.3,strokeColor=color,strokeWidth=1,roughness=0,
                       groupIds=[group] if group else [])
        spans=''.join(f'<tspan x="{x}" dy="{0 if i==0 else size*1.3}">{html.escape(line)}</tspan>' for i,line in enumerate(lines))
        self.svg.append(f'<text x="{x}" y="{y+size}" fill="{color}" font-family="Arial,sans-serif" font-size="{size}">{spans}</text>')
        return e
    def box(self,x,y,w,h,title,body='',fill='#eaf2f8',stroke='#476582',size=21):
        group=f'g{len(self.elements)}'
        self.element('rectangle',x,y,w,h,backgroundColor=fill,strokeColor=stroke,
                     roundness={'type':3},groupIds=[group])
        self.svg.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="12" fill="{fill}" stroke="{stroke}" stroke-width="2"/>')
        self.text(x+18,y+15,title,size,stroke,group)
        if body:self.text(x+18,y+52,body,17,'#334e68',group)
    def arrow(self,points,label='',lx=None,ly=None,dashed=False,color='#486581'):
        x,y=points[0];relative=[[a-x,b-y] for a,b in points]
        self.element('arrow',x,y,max(a for a,b in relative)-min(a for a,b in relative),
                     max(b for a,b in relative)-min(b for a,b in relative),points=relative,
                     lastCommittedPoint=None,startBinding=None,endBinding=None,startArrowhead=None,endArrowhead='arrow',
                     elbowed=False,strokeColor=color,strokeStyle='dashed' if dashed else 'solid')
        pts=' '.join(f'{a},{b}' for a,b in points)
        self.svg.append(f'<polyline points="{pts}" fill="none" stroke="{color}" stroke-width="2.5" '+('stroke-dasharray="8 6" ' if dashed else '')+'marker-end="url(#arrow)"/>')
        if label:self.text(lx if lx is not None else x+8,ly if ly is not None else y-30,label,16,color)
    def save(self,name):
        OUT.mkdir(parents=True,exist_ok=True)
        scene=dict(type='excalidraw',version=2,source='https://excalidraw.com',elements=self.elements,
                   appState=dict(gridSize=None,viewBackgroundColor='#ffffff'),files={})
        (OUT/(name+'.excalidraw')).write_text(json.dumps(scene,indent=2)+'\n')
        svg=f'<svg xmlns="http://www.w3.org/2000/svg" width="{self.w}" height="{self.h}" viewBox="0 0 {self.w} {self.h}"><defs><marker id="arrow" markerWidth="10" markerHeight="8" refX="8" refY="4" orient="auto" markerUnits="userSpaceOnUse"><path d="M0,0 L8,4 L0,8" fill="none" stroke="#486581" stroke-width="2"/></marker></defs><rect width="100%" height="100%" fill="white"/>'+''.join(self.svg)+'</svg>'
        (OUT/(name+'.svg')).write_text(svg)


def main():
    s=Scene('Docker delta delivery: architecture','Image transport stays outside Docker. Operator visibility stays on the sender.',h=1030)
    s.text(55,150,'SENDER / YOUR INFRASTRUCTURE',18,'#486581')
    s.text(1130,150,'EDGE / CUSTOMER DEVICE',18,'#486581')
    s.box(55,210,255,120,'Docker build + save','Actual images and layers\nExact image/config IDs')
    s.box(370,210,315,120,'Release publisher','Normalize layer compression\nChunk raw bytes; gzip chunks\nSign the ordered recipe')
    s.box(755,210,300,120,'HTTP origin','Immutable chunk objects\nSigned release manifests')
    s.box(1130,210,360,120,'Edge agent','Pull missing chunks only\nRetry on intermittent WAN')
    s.arrow([(310,270),(370,270)])
    s.arrow([(685,270),(755,270)])
    s.arrow([(1055,250),(1130,250)],'5 Mbit/s',1057,209)
    s.arrow([(1290,330),(1290,380),(905,380),(905,330)],'Outbound durable acknowledgement (staged / loaded)',770,393,True)
    s.box(1130,465,360,120,'Verified chunk cache','SHA-256; file sync; rename\nDirectory sync; keep progress')
    s.arrow([(1310,330),(1510,330),(1510,525),(1490,525)])
    s.box(1130,650,360,110,'Local reconstruction','Complete archive + SHA-256\nNo Docker action before this')
    s.arrow([(1310,585),(1310,650)])
    s.box(1130,840,360,105,'Existing Docker daemon','docker load; offline test run\nNo runtime replacement')
    s.arrow([(1310,760),(1310,840)])
    s.box(755,465,300,120,'Sender collector','Poll /stats and /receipts\nKeep SQLite history')
    s.arrow([(850,330),(720,330),(720,440),(905,440),(905,465)],'Sender endpoints',748,347)
    s.box(370,465,315,120,'Operator CLI','Live view; JSON; CSV\nNo SSH into the edge')
    s.arrow([(755,525),(685,525)])
    s.box(55,670,1000,145,'Two kinds of evidence','Observed: response-body bytes, requests, injected failures.\nAcknowledged: device says the exact archive was staged or Docker loaded it.\nNeither bytes served nor LOADED proves application health.','#fff8e6','#876925')
    s.text(55,865,'Demo: real Docker images + 32 KiB edit inside a large layer.\nCollector runs centrally. The edge exposes no inbound metrics port.\nAcknowledgement and collector control traffic are outside the chunk-byte counters.',19)
    s.save('architecture')

    s=Scene('Why a small image edit becomes a small transfer','Content-defined chunks are identified BEFORE compression; each chunk is compressed independently.',h=960)
    s.box(50,160,1450,115,'Existing image v1','One large uncompressed layer, not a newly added tiny layer. The device already has the verified chunks.')
    for i,label in enumerate(['A','B','C','D','E','F']):s.box(100+i*220,320,180,85,label,'cached','#e7f5ee','#2b7052',25)
    s.text(55,437,'Change 32 KiB inside the large file; rebuild image v2.',22)
    for i,label in enumerate(['A','B','X','D','E','F']):
        changed=label=='X';s.box(100+i*220,510,180,85,label,'fetch' if changed else 'reuse','#fff0dc' if changed else '#e7f5ee','#945817' if changed else '#2b7052',25)
    s.arrow([(630,595),(630,680),(965,680)],'Only missing chunk objects + signed manifest',280,638)
    s.box(995,640,485,110,'Reconstruct exact target archive','All chunks in manifest order\nVerify whole-archive SHA-256')
    s.box(55,785,1425,125,'What a changed hash means','A new layer digest does not make all bytes new. Chunk hashes expose reusable content inside that layer.\nThis is an independent lab CDC format, not native Docker registry delta support.\nCompression, tar metadata and chunk-boundary effects are measured rather than assumed.','#fff8e6','#876925')
    s.save('chunk-delta')

    s=Scene('Interrupted delivery and sender-side observability','Completion is acknowledged; transmitted bytes alone never imply successful import.',h=1080)
    for x,title,body in [(65,'Sender origin','Serves immutable chunks'),(570,'Edge agent','Keeps verified local cache'),(1120,'Docker','Not involved in WAN transfer')]:s.box(x,165,365,100,title,body)
    s.arrow([(430,325),(570,325)],'Chunk A completes',175,286)
    s.text(595,346,'Verify + durable commit A',18,'#2b7052')
    s.arrow([(430,430),(535,430)],'Chunk B is cut off',175,392)
    s.text(540,408,'X',32,'#ad342e')
    s.text(595,432,'Discard partial B; keep A',18)
    s.arrow([(570,510),(430,510)],'Reconnect / retry B only',120,470)
    s.arrow([(430,595),(570,595)],'Complete B',220,557)
    s.text(595,615,'Verify + commit B; assemble\nVerify complete archive',18,'#2b7052')
    s.arrow([(935,715),(1120,715)],'Local docker load',940,674)
    s.arrow([(1120,790),(935,790)],'Import succeeds',940,750)
    s.arrow([(570,870),(430,870)],'POST loaded acknowledgement',90,829,True)
    s.text(600,858,'Ack fails? Persist it; retry later.\nDuplicate ack is idempotent.',18)
    s.box(65,942,1420,95,'Operator CLI reads the sender collector','Bytes served = observed. Staged / loaded = device acknowledgement. Running / healthy requires a separate probe.','#fff8e6','#876925')
    s.save('retry-and-observability')
    print(f'Wrote 3 native Excalidraw scenes and SVG previews to {OUT}')

if __name__=='__main__':main()
