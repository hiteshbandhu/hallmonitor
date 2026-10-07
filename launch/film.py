"""The 30 s Hall Monitor film, cut to launch/out/drive.wav (128 BPM, 16 bars).

A 3D MacBook whose screen is built from the real app: the notch states the
app renders of itself (HallMonitor --snapshot), the menu bar menu, and the
terminal board as a live frame sequence. Camera moves are eased, everything
has motion blur, and the cuts land on the beat.

  1-2    the icon: bars light up on the beat, the name comes in
  3-4    kinetic type: the problem
  5-6    drop: into the MacBook, the notch wakes up
  7-8    the list opens; subagents; click one to jump to it
  9-10   needs you / finished
  11-12  the menu: plan limits
  13-14  the board in a terminal, then usage
  15     montage, a cut per beat
  16     end card

  Blender -b -P launch/film.py -- --stills 30,150,250,400,480,600,700,760,800,870
  Blender -b -P launch/film.py -- --anim [--from F --to F]
"""
import math
import os
import sys

import bpy

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
A = os.path.join(HERE, "out", "assets")
SEQ = os.path.join(HERE, "out", "seq")
argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []

FPS = 30
BPM = 128
BEAT_F = 60 / BPM * FPS        # 14.0625 frames
BAR_F = 4 * BEAT_F             # 56.25 frames
LAST = 899


def F(bar, beat=0.0):
    """Frame of a beat (bar 1-based, beat 0-based)."""
    return (bar - 1) * BAR_F + beat * BEAT_F


# Hard cuts: the frame each new shot starts on.
CUTS = [round(F(3)), round(F(5)), round(F(15)), round(F(15, 1)), round(F(15, 2)), round(F(15, 3)), round(F(16))]


# ---- easing ----
def clamp(x, a=0.0, b=1.0):
    return max(a, min(b, x))


def prog(f, f0, dur):
    return clamp((f - f0) / dur)


def out_expo(t):
    return 1 if t >= 1 else 1 - 2 ** (-10 * t)


def out_cubic(t):
    return 1 - (1 - t) ** 3


def in_out(t):
    return 4 * t ** 3 if t < 0.5 else 1 - (-2 * t + 2) ** 3 / 2


def in_out_quint(t):
    return 16 * t ** 5 if t < 0.5 else 1 - (-2 * t + 2) ** 5 / 2


def spring(t, bounce=0.25):
    if t <= 0:
        return 0.0
    if t >= 1:
        return 1.0
    return 1 - math.exp(-6.5 * t * (1 - bounce)) * math.cos(2 * math.pi * 1.35 * t)


def lerp(a, b, t):
    return a + (b - a) * t


def lerp3(a, b, t):
    return tuple(lerp(x, y, t) for x, y in zip(a, b))


def srgb(h):
    h = h.lstrip("#")
    c = [int(h[i:i + 2], 16) / 255 for i in (0, 2, 4)]
    return tuple(x / 12.92 if x <= 0.04045 else ((x + 0.055) / 1.055) ** 2.4 for x in c) + (1.0,)


# ---- scene ----
bpy.ops.wm.read_factory_settings(use_empty=True)
scene = bpy.context.scene
scene.render.engine = "BLENDER_EEVEE"
scene.render.fps = FPS
scene.frame_start, scene.frame_end = 0, LAST
scene.render.resolution_x, scene.render.resolution_y = 1920, 1080
scene.render.use_motion_blur = True
scene.render.motion_blur_shutter = 0.5
for owner in (scene.render, scene.eevee):
    try:
        owner.motion_blur_position = "START"  # no smear across hard cuts
    except (AttributeError, TypeError):
        pass
scene.eevee.motion_blur_steps = 4
scene.eevee.taa_render_samples = 32
scene.eevee.use_raytracing = True
scene.view_settings.view_transform = "Standard"
scene.view_settings.look = "None"
scene.render.image_settings.file_format = "PNG"
scene.render.filepath = os.path.join(HERE, "out", "film_frames", "")
world = bpy.data.worlds.new("W")
scene.world = world
world.use_nodes = True
world.node_tree.nodes["Background"].inputs[0].default_value = (0, 0, 0, 1)
col = scene.collection

try:
    ng = bpy.data.node_groups.new("Comp", "CompositorNodeTree")
    ng.interface.new_socket("Image", in_out="OUTPUT", socket_type="NodeSocketColor")
    rl = ng.nodes.new("CompositorNodeRLayers")
    gl = ng.nodes.new("CompositorNodeGlare")
    out = ng.nodes.new("NodeGroupOutput")
    gl.inputs["Type"].default_value = "Bloom"
    gl.inputs["Threshold"].default_value = 0.9
    gl.inputs["Strength"].default_value = 0.35
    ng.links.new(rl.outputs["Image"], gl.inputs["Image"])
    ng.links.new(gl.outputs["Image"], out.inputs[0])
    scene.compositing_node_group = ng
except Exception as e:
    print("compositor:", e)


# ---- materials & objects ----
def mat(name, color=None, image=None, strength=1.0, seq=None):
    """Emission with an animatable fade (alpha), for UI and type."""
    m = bpy.data.materials.new(name)
    m.use_nodes = True
    m.surface_render_method = "BLENDED"
    nt = m.node_tree
    nt.nodes.clear()
    o = nt.nodes.new("ShaderNodeOutputMaterial")
    em = nt.nodes.new("ShaderNodeEmission")
    em.inputs["Strength"].default_value = strength
    tr = nt.nodes.new("ShaderNodeBsdfTransparent")
    mx = nt.nodes.new("ShaderNodeMixShader")
    fade = nt.nodes.new("ShaderNodeValue")
    fade.outputs[0].default_value = 1
    mul = nt.nodes.new("ShaderNodeMath")
    mul.operation = "MULTIPLY"
    tx = None
    if image:
        tx = nt.nodes.new("ShaderNodeTexImage")
        tx.image = image
        tx.interpolation = "Cubic"
        tx.extension = "CLIP"
        if seq:
            tx.image_user.frame_duration = seq
            tx.image_user.frame_start = 0
            tx.image_user.use_cyclic = True
            tx.image_user.use_auto_refresh = True
        nt.links.new(tx.outputs["Color"], em.inputs["Color"])
        nt.links.new(tx.outputs["Alpha"], mul.inputs[0])
    else:
        em.inputs["Color"].default_value = color
        mul.inputs[0].default_value = 1
    nt.links.new(fade.outputs[0], mul.inputs[1])
    nt.links.new(mul.outputs[0], mx.inputs["Fac"])
    nt.links.new(tr.outputs[0], mx.inputs[1])
    nt.links.new(em.outputs[0], mx.inputs[2])
    nt.links.new(mx.outputs[0], o.inputs["Surface"])
    m["fade"] = fade.name
    return m, fade.outputs[0], tx


def plane(name, w, h, m, parent=None):
    me = bpy.data.meshes.new(name)
    me.from_pydata([(-w / 2, -h / 2, 0), (w / 2, -h / 2, 0), (w / 2, h / 2, 0), (-w / 2, h / 2, 0)], [], [(0, 1, 2, 3)])
    uv = me.uv_layers.new()
    for i, c in enumerate([(0, 0), (1, 0), (1, 1), (0, 1)]):
        uv.data[i].uv = c
    me.materials.append(m)
    o = bpy.data.objects.new(name, me)
    col.objects.link(o)
    if parent:
        o.parent = parent
    return o


def image(path):
    img = bpy.data.images.load(path)
    img.colorspace_settings.name = "sRGB"
    return img


def img_plane(name, path, width, parent=None, strength=1.0):
    img = image(path)
    h = width * img.size[1] / img.size[0]
    m, fade, _ = mat(name, image=img, strength=strength)
    return plane(name, width, h, m, parent), fade, h


def empty(name, parent=None):
    o = bpy.data.objects.new(name, None)
    col.objects.link(o)
    if parent:
        o.parent = parent
    return o


SF = bpy.data.fonts.load("/System/Library/Fonts/SFNS.ttf")
UB = bpy.data.fonts.load(os.path.expanduser("~/Library/Fonts/PPTelegraf-Ultrabold.otf"))
REG = bpy.data.fonts.load(os.path.expanduser("~/Library/Fonts/PPTelegraf-Regular.otf"))
MONO = bpy.data.fonts.load(os.path.expanduser("~/Library/Fonts/JetBrainsMono-Bold.ttf"))


def label(name, body, size, color, align="CENTER", font=UB, parent=None, strength=1.0):
    cu = bpy.data.curves.new(name, "FONT")
    cu.body = body
    cu.font = font
    cu.size = size
    cu.align_x = align
    cu.align_y = "CENTER"
    m, fade, _ = mat(name, color, strength=strength)
    cu.materials.append(m)
    o = bpy.data.objects.new(name, cu)
    col.objects.link(o)
    if parent:
        o.parent = parent
    return o, fade


def key(o, fade, fn, f0=0, f1=LAST):
    """Key fn(f) -> {loc, rot, scale, alpha} on every frame, plus a key just
    before each hard cut so nothing interpolates across it."""
    frames = [float(f) for f in range(f0, f1 + 1)] + [c - 0.02 for c in CUTS if f0 < c <= f1]
    for f in sorted(frames):
        p = fn(f if f == int(f) else math.floor(f))
        if "loc" in p:
            o.location = p["loc"]
            o.keyframe_insert("location", frame=f)
        if "rot" in p:
            o.rotation_euler = p["rot"]
            o.keyframe_insert("rotation_euler", frame=f)
        if "scale" in p:
            s = p["scale"]
            o.scale = (s, s, s) if isinstance(s, (int, float)) else s
            o.keyframe_insert("scale", frame=f)
        if "alpha" in p and fade is not None:
            fade.default_value = clamp(p["alpha"])
            fade.keyframe_insert("default_value", frame=f)


def window(f, f0, f1, fin=4, fout=4):
    """1 between f0 and f1, with short fades; 0 outside."""
    return clamp((f - f0 + 1) / fin) * (1 - clamp((f - f1) / fout))


# ---- camera ----
cam = bpy.data.objects.new("Cam", bpy.data.cameras.new("Cam"))
cam.data.lens = 50
cam.data.sensor_width = 36
cam.data.clip_start = 0.05
cam.data.clip_end = 2000
col.objects.link(cam)
scene.camera = cam
target = empty("CamTarget")
cam.rotation_mode = "QUATERNION"
cam.data.dof.use_dof = True
cam.data.dof.focus_object = target
cam.data.dof.aperture_fstop = 5.6

# ---- 1. the icon, in a void at x = -300 ----
ICON_AT = (-300.0, 0.0, 0.0)
import icon as icon_mod  # noqa: E402  (the same model as the app icon)

icon_objs, bar_nodes, dot_node = icon_mod.build()
icon_root = empty("icon_root")
icon_root.location = ICON_AT
for o in icon_objs:
    if o.parent is None:
        o.parent = icon_root


def area(name, loc, rot, energy, size, color=(1, 1, 1)):
    d = bpy.data.lights.new(name, "AREA")
    d.energy = energy
    d.size = size
    d.color = color
    o = bpy.data.objects.new(name, d)
    o.location = loc
    o.rotation_euler = rot
    col.objects.link(o)
    return o


x0, y0, z0 = ICON_AT
area("ikey", (x0, y0 + 7, 10), (math.radians(-35), 0, 0), 500, 14)
area("ifill", (x0 - 7, y0 - 4, 6), (math.radians(30), math.radians(-45), 0), 160, 6, (0.75, 0.85, 1))
area("irim", (x0 + 5, y0 + 2, 3), (math.radians(-10), math.radians(60), 0), 220, 3, (1, 0.9, 0.75))

# Bars light up one per beat; the dot pops on beat 3 of bar 2.
for i, p in enumerate(bar_nodes):
    sock = p.inputs["Emission Strength"]
    on = [F(1, 0), F(1, 2), F(2, 0)][i]
    for f in range(0, round(F(3)) + 1):
        k = out_expo(prog(f, on, 10))
        sock.default_value = 0.05 + 0.5 * k + 1.6 * math.exp(-max(0, f - on) / 5) * (f >= on)
        sock.keyframe_insert("default_value", frame=f)
# (the icon no longer has a dot)

# The icon turns slowly toward us; its name comes in under it.
key(icon_root, None, lambda f: {"rot": (math.radians(lerp(14, 4, out_cubic(prog(f, 0, 110)))),
                                        math.radians(lerp(-28, -6, out_cubic(prog(f, 0, 110)))), 0),
                                "loc": (x0, y0 + 1.2 * out_expo(prog(f, F(2), 20)), z0)},
    0, round(F(3)))
name_o, name_f = label("i_name", "Hall Monitor", 1.35, srgb("#f5f5f7"))
name_o.location = (x0, y0 - 5.0, 0)
key(name_o, name_f, lambda f: {"loc": (x0, y0 - 5.2 + 0.5 * out_expo(prog(f, F(2), 18)), 0.5),
                               "alpha": out_expo(prog(f, F(2), 14))}, 0, round(F(3)))
tag_o, tag_f = label("i_tag", "A hall monitor for your coding agents.", 0.52, srgb("#a1a1a6"), font=REG)
key(tag_o, tag_f, lambda f: {"loc": (x0, y0 - 6.55 + 0.35 * out_expo(prog(f, F(2, 2), 18)), 0.5),
                             "alpha": out_expo(prog(f, F(2, 2), 14))}, 0, round(F(3)))

# ---- 2. kinetic type, in a void at x = +300 ----
TYPE_AT = (300.0, 0.0, 0.0)
words = [
    (F(3, 0), F(3, 2), "Five agents.", "#f5f5f7"),
    (F(3, 2), F(4, 0), "Three machines.", "#f5f5f7"),
    (F(4, 0), F(4, 1), "Who's done?", "#4ade80"),
    (F(4, 1), F(4, 2), "Who's stuck?", "#f87171"),
    (F(4, 2), F(4, 3), "Who needs you?", "#fbbf24"),
]
for i, (a, b, text, c) in enumerate(words):
    o, fd = label(f"w{i}", text, 1.3, srgb(c))
    key(o, fd, lambda f, a=a, b=b: {
        "loc": (TYPE_AT[0], -0.15 * (1 - out_expo(prog(f, a, 8))), 0),
        "scale": lerp(1.25, 1.0, out_expo(prog(f, a, 10))) * (1 + 0.04 * prog(f, a, b - a)),
        "alpha": 1.0 if a <= f < b else 0.0}, round(F(3)) - 1, round(F(5)))

# ---- 3. the MacBook ----
SW, SH = 16.0, 10.35        # screen
PT = SW / 1512              # a 14" MacBook Pro is 1512 x 982 pt
NOTCH_H = 32 * PT
LID_W, LID_H, LID_T = 16.8, 11.0, 0.14
BASE_W, BASE_D, BASE_T = 16.8, 11.6, 0.42
HINGE = (0.0, BASE_T, -BASE_D / 2 + 0.25)


def rounded_box(name, w, h, d, r, segs=12, parent=None):
    bpy.ops.mesh.primitive_cube_add(size=1)
    o = bpy.context.active_object
    o.name = name
    for v in o.data.vertices:
        v.co.x *= w
        v.co.y *= h
        v.co.z *= d
    b = o.modifiers.new("bevel", "BEVEL")
    b.width = r
    b.segments = segs
    b.use_clamp_overlap = False
    bpy.ops.object.shade_smooth()
    if parent:
        o.parent = parent
    return o


def rounded_slab(name, w, h, t, r, parent=None, edge=0.03):
    """A w x h rectangle with corner radius r, t thick (along local Z, from
    -t to 0), with softly rounded edges. Unlike beveling a thin box, this
    never folds over itself."""
    bpy.ops.mesh.primitive_plane_add(size=1)
    o = bpy.context.active_object
    o.name = name
    for v in o.data.vertices:
        v.co.x *= w
        v.co.y *= h
    corners = o.modifiers.new("corners", "BEVEL")
    corners.affect = "VERTICES"
    corners.width = r
    corners.segments = 16
    sol = o.modifiers.new("thick", "SOLIDIFY")
    sol.thickness = t
    sol.offset = -1
    if edge:
        e = o.modifiers.new("edge", "BEVEL")
        e.width = min(edge, t * 0.45)
        e.segments = 4
        e.limit_method = "ANGLE"
    bpy.ops.object.shade_smooth()
    if parent:
        o.parent = parent
    return o


def principled(name, color, metallic=0.0, rough=0.4, coat=0.0):
    m = bpy.data.materials.new(name)
    m.use_nodes = True
    p = m.node_tree.nodes["Principled BSDF"]
    p.inputs["Base Color"].default_value = color
    p.inputs["Metallic"].default_value = metallic
    p.inputs["Roughness"].default_value = rough
    p.inputs["Coat Weight"].default_value = coat
    return m


alu = principled("alu", srgb("#2e3036"), 1.0, 0.32)
black = principled("black", srgb("#050505"), 0.0, 0.2, 0.6)
keys_m = principled("keys", srgb("#0d0e10"), 0.0, 0.6)

base = rounded_slab("base", BASE_W, BASE_D, BASE_T, 0.5, edge=0.12)
base.rotation_euler = (math.radians(-90), 0, 0)
base.location = (0, BASE_T, 0)
base.data.materials.append(alu)
kb = rounded_slab("keyboard", 13.8, 5.2, 0.02, 0.12, edge=0)
kb.rotation_euler = (math.radians(-90), 0, 0)
kb.location = (0, BASE_T + 0.012, -1.6)
kb.data.materials.append(keys_m)
tp = rounded_slab("trackpad", 6.8, 4.0, 0.01, 0.3, edge=0)
tp.rotation_euler = (math.radians(-90), 0, 0)
tp.location = (0, BASE_T + 0.006, 3.2)
tp.data.materials.append(principled("pad", srgb("#26282d"), 1.0, 0.25))

hinge = empty("hinge")
hinge.location = HINGE
lid = rounded_slab("lid", LID_W, LID_H, LID_T, 0.45, parent=hinge, edge=0.05)
lid.location = (0, LID_H / 2, 0)
lid.data.materials.append(alu)
glass = rounded_slab("glass", LID_W - 0.04, LID_H - 0.04, 0.004, 0.43, parent=hinge, edge=0)
glass.location = (0, LID_H / 2, 0.003)
glass.data.materials.append(black)
screen = empty("screen", hinge)
SCREEN_CY = LID_H / 2 - 0.05
screen.location = (0, SCREEN_CY, 0.006)


def lid_angle(f):
    """Degrees the lid leans back from vertical; it opens during the build."""
    t = in_out_quint(prog(f, F(3, 2), F(5) - F(3, 2) - 6))
    return lerp(-88, 12, t)


key(hinge, None, lambda f: {"rot": (math.radians(-lid_angle(f)), 0, 0)})


def screen_world(x, y, z, f):
    """World position of a point on the screen (screen coords) at frame f."""
    a = math.radians(-lid_angle(f))
    ly, lz = SCREEN_CY + y, 0.006 + z
    wy = ly * math.cos(a) - lz * math.sin(a)
    wz = ly * math.sin(a) + lz * math.cos(a)
    return (HINGE[0] + x, HINGE[1] + wy, HINGE[2] + wz)


def screen_normal(f):
    a = math.radians(-lid_angle(f))
    return (0.0, -math.sin(a), math.cos(a))


# Studio: a dark glossy floor, soft lights.
floor_m = principled("floor", srgb("#060607"), 0.0, 0.18)
fl = plane("floor", 400, 400, floor_m)
fl.rotation_euler = (math.radians(-90), 0, 0)
area("key", (0, 18, 12), (math.radians(-50), 0, 0), 2600, 18)
area("rimL", (-16, 8, -10), (math.radians(-20), math.radians(-130), 0), 1400, 8, (0.6, 0.7, 1.0))
area("rimR", (16, 8, -10), (math.radians(-20), math.radians(130), 0), 1400, 8, (1.0, 0.75, 0.6))

# ---- the screen's content (all emission; switches on at the drop) ----
ON = F(5)


def on(f):
    return out_cubic(prog(f, ON - 1, 5))


wall_m = bpy.data.materials.new("wall")
wall_m.use_nodes = True
nt = wall_m.node_tree
nt.nodes.clear()
o_ = nt.nodes.new("ShaderNodeOutputMaterial")
tco = nt.nodes.new("ShaderNodeTexCoord")
sep = nt.nodes.new("ShaderNodeSeparateXYZ")
rp = nt.nodes.new("ShaderNodeValToRGB")
rp.color_ramp.interpolation = "EASE"
rp.color_ramp.elements[0].color = srgb("#0b1022")
rp.color_ramp.elements[1].color = srgb("#3a4f9a")
em = nt.nodes.new("ShaderNodeEmission")
wall_strength = em.inputs["Strength"]
nt.links.new(tco.outputs["UV"], sep.inputs[0])
nt.links.new(sep.outputs["Y"], rp.inputs["Fac"])
nt.links.new(rp.outputs["Color"], em.inputs["Color"])
nt.links.new(em.outputs[0], o_.inputs["Surface"])
wall = plane("wall", SW, SH, wall_m, screen)
for f in range(0, LAST + 1):
    wall_strength.default_value = on(f) * (1 + 0.6 * math.exp(-max(0, f - ON) / 4))  # flash on
    wall_strength.keyframe_insert("default_value", frame=f)


def glow(name, color, size, strength, parent):
    m = bpy.data.materials.new(name)
    m.use_nodes = True
    m.surface_render_method = "BLENDED"
    nt = m.node_tree
    nt.nodes.clear()
    o = nt.nodes.new("ShaderNodeOutputMaterial")
    tc_ = nt.nodes.new("ShaderNodeTexCoord")
    mp = nt.nodes.new("ShaderNodeMapping")
    mp.inputs["Location"].default_value = (-1, -1, 0)
    mp.inputs["Scale"].default_value = (2, 2, 2)
    gr = nt.nodes.new("ShaderNodeTexGradient")
    gr.gradient_type = "SPHERICAL"
    rp_ = nt.nodes.new("ShaderNodeValToRGB")
    rp_.color_ramp.interpolation = "EASE"
    e = nt.nodes.new("ShaderNodeEmission")
    e.inputs["Color"].default_value = color
    e.inputs["Strength"].default_value = strength
    tr = nt.nodes.new("ShaderNodeBsdfTransparent")
    mx = nt.nodes.new("ShaderNodeMixShader")
    fade = nt.nodes.new("ShaderNodeValue")
    mul = nt.nodes.new("ShaderNodeMath")
    mul.operation = "MULTIPLY"
    nt.links.new(tc_.outputs["UV"], mp.inputs["Vector"])
    nt.links.new(mp.outputs["Vector"], gr.inputs["Vector"])
    nt.links.new(gr.outputs["Fac"], rp_.inputs["Fac"])
    nt.links.new(rp_.outputs["Color"], mul.inputs[0])
    nt.links.new(fade.outputs[0], mul.inputs[1])
    nt.links.new(mul.outputs[0], mx.inputs["Fac"])
    nt.links.new(tr.outputs[0], mx.inputs[1])
    nt.links.new(e.outputs[0], mx.inputs[2])
    nt.links.new(mx.outputs[0], o.inputs["Surface"])
    return plane(name, size, size, m, parent), fade.outputs[0]


for i, (c, x, y, s, k) in enumerate([("#8b6cff", -4.6, -1.5, 9, 0.8), ("#2dd4bf", 5.0, -3.0, 10, 0.6),
                                     ("#e0845f", 0.4, -4.4, 8, 0.6)]):
    g, gf = glow(f"glow{i}", srgb(c), s, k, screen)
    key(g, gf, lambda f, x=x, y=y, i=i: {"loc": (x + 0.6 * math.sin(f * 0.02 + i * 2), y + 0.4 * math.cos(f * 0.017 + i), 0.002),
                                         # off once the terminal is up: blended
                                         # planes sort per object, not per pixel
                                         "alpha": on(f) * (1 - prog(f, F(13) - 6, 6))})
    g.scale = (1, 1, 1)

# Menu bar.
TOP = SH / 2
mb_m, mb_f, _ = mat("menubar", srgb("#5a6696"))
mb = plane("menubar", SW, NOTCH_H, mb_m, screen)
key(mb, mb_f, lambda f: {"loc": (0, TOP - NOTCH_H / 2, 0.004), "alpha": 0.5 * on(f)})
MB_Y = TOP - NOTCH_H / 2
menus, mf = label("menus", "Finder    File    Edit    View    Go    Window    Help", 13 * PT, srgb("#f2f2f5"), "LEFT", SF, screen)
key(menus, mf, lambda f: {"loc": (-SW / 2 + 40 * PT, MB_Y, 0.006), "alpha": on(f)})
clock, cf = label("clock", "Fri 9:41 AM", 13 * PT, srgb("#f2f2f5"), "RIGHT", SF, screen)
key(clock, cf, lambda f: {"loc": (SW / 2 - 14 * PT, MB_Y, 0.006), "alpha": on(f)})
GLYPH_X = SW / 2 - 150 * PT
for i, h in enumerate([7, 12, 9]):
    bm, bf, _ = mat(f"glyph{i}", srgb("#f2f2f5"))
    b = plane(f"glyph{i}", 3.4 * PT, h * PT, bm, screen)
    key(b, bf, lambda f, i=i, h=h: {"loc": (GLYPH_X + i * 5.3 * PT, MB_Y - 6 * PT + h * PT / 2, 0.006), "alpha": on(f)})
gc, gcf = label("glyph_n", "3", 13 * PT, srgb("#f2f2f5"), "LEFT", SF, screen)
key(gc, gcf, lambda f: {"loc": (GLYPH_X + 15 * PT, MB_Y, 0.006), "alpha": on(f)})

# Notch states (real app renders), anchored at the top edge.
CANVAS_W = 536 * PT


def state(name, fn, z):
    o, fade, h = img_plane(name, os.path.join(A, fn), CANVAS_W, screen)
    return o, fade, h, z


def show(st, f0, f1, sx0, sy0, dur=16, fin=3, fout=3):
    o, fade, h, z = st

    def fn(f):
        k = spring(prog(f, f0, dur))
        sx, sy = lerp(sx0, 1, k), lerp(sy0, 1, k)
        return {"loc": (0, TOP - h * sy / 2, z), "scale": (sx, sy, 1), "alpha": window(f, f0, f1, fin, fout)}
    key(o, fade, fn)


hidden = state("n_hidden", "notch_hidden.png", 0.020)
compact = state("n_compact", "notch_compact.png", 0.021)
expanded = state("n_expanded", "notch_expanded.png", 0.022)
needs = state("n_needs", "notch_banner_needs.png", 0.023)
done = state("n_done", "notch_banner_done.png", 0.024)
expanded2 = state("n_expanded2", "notch_expanded.png", 0.025)   # montage
needs2 = state("n_needs2", "notch_banner_needs.png", 0.026)      # montage

EARS = F(5, 1)
LIST = F(7, 0.5)
FOLD = F(8, 3)
NEEDS = F(9)
DONE = F(10)
key(hidden[0], hidden[1], lambda f: {"loc": (0, TOP - hidden[2] / 2, hidden[3]), "alpha": 1.0})
show(compact, EARS, LIST + 2, 0.72, 1.0)
show(expanded, LIST, FOLD, 0.8, 0.22)
# Back to the ears after the fold, until the banners.
c2 = state("n_compact2", "notch_compact.png", 0.0215)
show(c2, FOLD, NEEDS + 2, 1.2, 1.6, dur=14)
show(needs, NEEDS, DONE, 0.78, 0.32)
show(done, DONE, F(11), 0.96, 0.96, dur=12)
c3 = state("n_compact3", "notch_compact.png", 0.0216)
show(c3, F(11), LAST + 10, 1.0, 1.0)
show(expanded2, F(15, 0), F(15, 1) - 1, 0.85, 0.3, dur=10, fin=1, fout=1)
show(needs2, F(15, 3), F(16) - 1, 0.8, 0.35, dur=10, fin=1, fout=1)

# Cursor: glides up to the notch, hovers the list, clicks a row.
cur_img = image(os.path.join(A, "cursor.png"))
cur_m, cur_f, _ = mat("cursor", image=cur_img)
cur = plane("cursor", 26 * PT * 0.9, 38 * PT * 0.9, cur_m, screen)
ROW_Y = TOP - 210 * PT   # "Flaky e2e on checkout"


def cursor_fn(f):
    p0, p1, p2 = (2.4, -1.6), (0.25, TOP - 0.18), (0.6, ROW_Y)
    k1 = in_out(prog(f, F(7) - 4, 16))
    k2 = in_out(prog(f, F(8, 1), 12))
    x, y = lerp(p0[0], p1[0], k1), lerp(p0[1], p1[1], k1)
    x, y = lerp(x, p2[0], k2), lerp(y, p2[1], k2)
    press = 1 - 0.12 * math.exp(-((f - F(8, 2)) ** 2) / 4)
    w = 26 * PT * 0.9
    return {"loc": (x + w / 2 * press, y - 38 * PT * 0.9 / 2 * press, 0.04), "scale": press,
            "alpha": window(f, F(7) - 6, FOLD + 6, 6, 8)}


key(cur, cur_f, cursor_fn)
# Click ripple.
rip_m, rip_f, _ = mat("ripple", srgb("#ffffff"), strength=1.5)
bpy.ops.mesh.primitive_torus_add(major_radius=1, minor_radius=0.02, major_segments=96, minor_segments=8)
rip = bpy.context.active_object
rip.name = "ripple"
rip.parent = screen
rip.data.materials.append(rip_m)
key(rip, rip_f, lambda f: {"loc": (0.6, ROW_Y, 0.039), "scale": (0.05 + 0.6 * out_expo(prog(f, F(8, 2), 12)),) * 2 + (0.05,),
                           "alpha": (f >= F(8, 2)) * (1 - prog(f, F(8, 2), 12))})

# The menu (an exact rebuild of the real NSMenu, demo data).
menu_o, menu_f, menu_h = img_plane("menu", os.path.join(A, "menu_film.png"), 450 * PT * 1.0, screen)
MENU_X = GLYPH_X - 20 * PT + 450 * PT / 2


def menu_fn(f):
    a = window(f, F(11, 0.25), F(13) - 4, 2, 6) + window(f, F(15, 2), F(15, 3) - 1, 1, 1)
    k = out_expo(prog(f, F(11, 0.25), 8))
    return {"loc": (MENU_X, TOP - NOTCH_H - menu_h / 2 + 0.03 * (1 - k), 0.03), "alpha": a}


key(menu_o, menu_f, menu_fn)

# The terminal board, playing back the real TUI frame by frame.
board_img = image(os.path.join(SEQ, "board_0000_win.png"))
board_img.source = "SEQUENCE"
bm_, board_f, board_tx = mat("board", image=board_img, seq=48)
BW = 15.2
bh = BW * board_img.size[1] / board_img.size[0]
board = plane("board", BW, bh, bm_, screen)
BOARD_IN = F(13)


def board_fn(f):
    k = spring(prog(f, BOARD_IN, 18), 0.18)
    s = lerp(0.82, 1.0, k)
    return {"loc": (0, -0.35 - 0.6 * (1 - k), 0.012), "scale": s, "alpha": window(f, BOARD_IN, LAST, 3, 1)}


key(board, board_f, board_fn)
iu = board_tx.image_user
for f in range(0, LAST + 1):
    iu.frame_offset = (f // 3) % 48 - f  # the board draws ~10 fps
    iu.keyframe_insert("frame_offset", frame=f)
usage_o, usage_f, usage_h = img_plane("usage", os.path.join(SEQ, "usage_win.png"), 13.5, screen)
key(usage_o, usage_f, lambda f: {"loc": (1.2, -0.7 - 0.5 * (1 - out_expo(prog(f, F(14), 14))), 0.016),
                                 "scale": lerp(0.9, 1.0, out_expo(prog(f, F(14), 14))),
                                 "alpha": window(f, F(14), F(15) - 1, 4, 1)})

# ---- captions: fixed to the camera, lower left, Apple keynote style ----
CAP = [
    (F(5, 3), F(7) - 2, "It lives in your notch."),
    (F(7, 2), F(8, 1.5), "Every agent. Its subagents."),
    (F(8, 2.2), F(9) - 2, "Click one. You're there."),
    (F(9, 0.5), F(10) - 1, "Knows when one needs you."),
    (F(10, 0.5), F(11) - 2, "And when one's done."),
    (F(11, 1), F(13) - 3, "Plan limits. Live."),
    (F(13, 1), F(14) - 2, "Every agent, every machine."),
    (F(14, 1), F(15) - 2, "And what it all used."),
]
for i, (a, b, text) in enumerate(CAP):
    o, fd = label(f"cap{i}", text, 0.19, srgb("#f5f5f7"), "LEFT", UB, cam, strength=1.0)

    def cap_fn(f, a=a, b=b):
        k = out_expo(prog(f, a, 12))
        return {"loc": (-1.62, -0.84 + 0.06 * (1 - k), -5.0), "alpha": window(f, a, b, 6, 5)}
    key(o, fd, cap_fn)

# ---- 9. end card, in a void at x = -300 (behind the icon's set, lower) ----
END_AT = (-300.0, -40.0, 0.0)
end_icon, end_bars, end_dot = None, None, None
e_objs, e_glows, _ = icon_mod.build()
end_root = empty("end_root")
end_root.location = (END_AT[0], END_AT[1] + 1.6, 0)
for o in e_objs:
    if o.parent is None:
        o.parent = end_root
for p in e_glows:
    p.inputs["Emission Strength"].default_value = 0.55
end_root.scale = (0.42, 0.42, 0.42)
ex, ey, _ = END_AT
area("ekey", (ex, ey + 9, 10), (math.radians(-35), 0, 0), 500, 14)
area("erim", (ex + 5, ey + 4, 3), (math.radians(-10), math.radians(60), 0), 220, 3, (1, 0.9, 0.75))
word, wf = label("e_word", "hallmonitor", 1.25, srgb("#f5f5f7"), font=UB)
cmd, cmdf = label("e_cmd", "brew install hiteshbandhu/tap/hallmonitor", 0.36, srgb("#d1d1d6"), font=MONO)
sub, subf = label("e_sub", "Claude Code  ·  Codex  ·  macOS & Linux  ·  open source", 0.3, srgb("#8e8e93"), font=SF)
key(end_root, None, lambda f: {"loc": (ex, ey + 1.55 + 0.4 * (1 - out_expo(prog(f, F(16), 16))), 0),
                               "rot": (0, math.radians(lerp(-18, 0, out_cubic(prog(f, F(16), 30)))), 0),
                               "scale": 0.42 * lerp(0.8, 1, spring(prog(f, F(16), 20)))}, round(F(16)) - 1, LAST)
key(word, wf, lambda f: {"loc": (ex, ey - 1.35 + 0.3 * (1 - out_expo(prog(f, F(16, 0.5), 14))), 0.3),
                         "alpha": out_expo(prog(f, F(16, 0.5), 10))}, round(F(16)) - 1, LAST)
key(cmd, cmdf, lambda f: {"loc": (ex, ey - 2.6, 0.3), "alpha": out_expo(prog(f, F(16, 1.5), 10))}, round(F(16)) - 1, LAST)
key(sub, subf, lambda f: {"loc": (ex, ey - 3.3, 0.3), "alpha": out_expo(prog(f, F(16, 2.2), 10))}, round(F(16)) - 1, LAST)


# ---- the camera, shot by shot ----
def look(at_screen, dist, f, offset=(0, 0, 0)):
    """Camera at `dist` along the screen's normal from a screen point."""
    p = screen_world(at_screen[0], at_screen[1], 0, f)
    n = screen_normal(f)
    c = tuple(p[i] + n[i] * dist + offset[i] for i in range(3))
    return c, p


def camera(f):
    if f < F(3):  # icon
        t = prog(f, 0, F(3))
        return (ICON_AT[0] + lerp(-3.0, 1.0, in_out(t)), lerp(-2.5, -1.2, t), lerp(40, 34, out_cubic(t))), \
            (ICON_AT[0], -0.9, 0), 0
    if f < F(5):  # type
        return (TYPE_AT[0], 0, 14), TYPE_AT, 0
    if f < F(7):  # the drop: swoop in from a wide 3/4 view to the notch
        t = out_expo(prog(f, F(5), 44))
        wide = (-15.0, 9.0, 16.0)
        near, aim = look((0, TOP - 1.9), 11.5, f)
        p = lerp3(wide, near, t)
        return p, lerp3(screen_world(0, 0, 0, f), aim, out_cubic(prog(f, F(5), 36))), 0
    if f < F(11):  # notch close-up, a slow push
        t = in_out(prog(f, F(7), F(11) - F(7)))
        c, aim = look((0, TOP - 1.9 + 0.2 * t), lerp(11.5, 10.2, t), f, (lerp(0, -0.6, t), 0, 0))
        return c, aim, 0
    if f < F(13):  # slide over to the menu
        t = in_out(prog(f, F(11), 18))
        a = (0, TOP - 1.7)
        b = (MENU_X - 0.5, TOP - 3.2)
        at = (lerp(a[0], b[0], t), lerp(a[1], b[1], t))
        return look(at, lerp(10.2, 12.0, t), f, (lerp(0, 1.5, t), 0, 0)) + (0,)
    if f < F(15):  # pull back to see the whole machine, board on screen, then in
        t = in_out(prog(f, F(13), 30))
        t2 = in_out(prog(f, F(14), F(15) - F(14)))
        far = (13.0, 10.0, 21.0)
        close, aim = look((0, -0.6), 15.0, f, (2.5, 0.5, 0))
        p = lerp3(close, far, t)
        p = lerp3(p, look((0.8, -0.6), 13.0, f, (-1.5, 0, 0))[0], t2)
        return p, lerp3(aim, screen_world(0.8, -0.6, 0, f), t2), 0
    if f < F(16):  # montage: a cut per beat, each a quick push
        beat = int((f - F(15)) // BEAT_F)
        t = out_cubic(prog(f, F(15, beat), BEAT_F))
        shots = [
            ((0, TOP - 1.7), 9.5, 8.6, (-2.0, 0.6, 0)),     # the list
            ((-3.5, 0.6), 7.5, 6.6, (2.5, -0.8, 0)),        # cards on the board
            ((MENU_X, TOP - 2.6), 9.0, 8.0, (1.8, 0.4, 0)),  # the menu
            ((0, TOP - 0.9), 7.0, 6.2, (-1.2, -0.3, 0)),    # needs you
        ]
        at, d0, d1, off = shots[beat]
        c, aim = look(at, lerp(d0, d1, t), f, tuple(o * (1 - 0.3 * t) for o in off))
        return c, aim, 0
    t = prog(f, F(16), LAST - F(16))  # end card
    return (END_AT[0] + lerp(0.6, 0, out_cubic(t)), END_AT[1] - 0.7, lerp(23.5, 21.0, out_cubic(t))), \
        (END_AT[0], END_AT[1] - 0.7, 0), 0


from mathutils import Matrix, Vector  # noqa: E402


def look_rotation(eye, at, roll=0.0):
    """Camera rotation looking from eye to at, with world +Y up (this scene
    is Y-up), as a quaternion."""
    z = (Vector(eye) - Vector(at)).normalized()      # camera looks down -Z
    x = Vector((0, 1, 0)).cross(z).normalized()
    y = z.cross(x)
    m = Matrix((x, y, z)).transposed()
    if roll:
        m = m @ Matrix.Rotation(roll, 3, "Z")
    return m.to_quaternion()


prev = [None]


def cam_fn(f):
    eye, at, roll = camera(f)
    q = look_rotation(eye, at, roll)
    if prev[0] is not None and prev[0].dot(q) < 0:
        q.negate()  # keep the shortest path between keys
    prev[0] = q
    return {"loc": eye, "quat": q}


frames = sorted([float(f) for f in range(0, LAST + 1)] + [c - 0.02 for c in CUTS])
for f in frames:
    p = cam_fn(f if f == int(f) else math.floor(f))
    cam.location = p["loc"]
    cam.keyframe_insert("location", frame=f)
    cam.rotation_quaternion = p["quat"]
    cam.keyframe_insert("rotation_quaternion", frame=f)
key(target, None, lambda f: {"loc": camera(f)[1]})

# Montage: board in front on beat 2 only, menu on beat 3 (keyed above).
bpy.ops.wm.save_as_mainfile(filepath=os.path.join(HERE, "out", "film.blend"))

if "--stills" in argv:
    d = os.path.join(HERE, "out", "film_stills")
    os.makedirs(d, exist_ok=True)
    scene.render.resolution_percentage = int(argv[argv.index("--pct") + 1]) if "--pct" in argv else 50
    for f in [int(x) for x in argv[argv.index("--stills") + 1].split(",")]:
        scene.frame_set(f)
        scene.render.filepath = os.path.join(d, f"{f:04d}.png")
        bpy.ops.render.render(write_still=True)
elif "--anim" in argv:
    if "--from" in argv:
        scene.frame_start = int(argv[argv.index("--from") + 1])
    if "--to" in argv:
        scene.frame_end = int(argv[argv.index("--to") + 1])
    bpy.ops.render.render(animation=True)
