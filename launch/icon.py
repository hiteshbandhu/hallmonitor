"""Hall Monitor's app icon, rendered in Blender.

    blender -b -P launch/icon.py -- out.png [size]

A graphite squircle on Apple's macOS icon grid (an 824 px tile on a 1024
canvas), carrying the menu bar glyph as three raised, glowing bars.
"""
import math
import sys

import bpy


TILE = 8.24  # Blender units; 824 px of a 1024 px icon


def srgb(h):
    h = h.lstrip("#")
    c = [int(h[i:i + 2], 16) / 255 for i in (0, 2, 4)]
    return tuple(x / 12.92 if x <= 0.04045 else ((x + 0.055) / 1.055) ** 2.4 for x in c) + (1,)


def rounded_box(name, w, h, d, r, segs=24):
    bpy.ops.mesh.primitive_cube_add(size=1)
    o = bpy.context.active_object
    o.name = name
    o.scale = (w, h, d)
    bpy.ops.object.transform_apply(scale=True)
    m = o.modifiers.new("bevel", "BEVEL")
    m.width = r
    m.segments = segs
    m.limit_method = "NONE"
    m.use_clamp_overlap = False
    m.affect = "EDGES"
    bpy.ops.object.shade_smooth()
    return o


def principled(name, **kw):
    m = bpy.data.materials.new(name)
    m.use_nodes = True
    p = m.node_tree.nodes["Principled BSDF"]
    for k, v in kw.items():
        p.inputs[k].default_value = v
    return m, p


def build():
    """Adds the icon (tile and bars) to the current scene, centered on the
    origin facing +Z, 8.24 units across. Returns the objects, and the bars'
    and dot's Principled nodes (to animate their glow)."""
    scene = bpy.context.scene
    glows = []
    bars = []
    # ---- the tile: an Apple-style squircle (superellipse), thin, softly edged ----


    def squircle(name, size, depth, n=5.0, pts=256):
        import bmesh
        me = bpy.data.meshes.new(name)
        bm = bmesh.new()
        a = size / 2
        verts = []
        for k in range(pts):
            t = 2 * math.pi * k / pts
            c, s_ = math.cos(t), math.sin(t)
            x = a * math.copysign(abs(c) ** (2 / n), c)
            y = a * math.copysign(abs(s_) ** (2 / n), s_)
            verts.append(bm.verts.new((x, y, 0)))
        face = bm.faces.new(verts)
        r = bmesh.ops.extrude_face_region(bm, geom=[face])
        for v in r["geom"]:
            if isinstance(v, bmesh.types.BMVert):
                v.co.z -= depth
        bm.normal_update()
        bm.to_mesh(me)
        bm.free()
        o = bpy.data.objects.new(name, me)
        scene.collection.objects.link(o)
        bpy.context.view_layer.objects.active = o
        o.select_set(True)
        m = o.modifiers.new("edge", "BEVEL")
        m.width = 0.16
        m.segments = 10
        m.limit_method = "ANGLE"
        m.angle_limit = math.radians(50)
        bpy.ops.object.shade_smooth()
        return o


    tile = squircle("tile", TILE, 0.5)
    tm, tp = principled("tile", **{"Roughness": 0.32, "Metallic": 0.0, "Coat Weight": 0.6, "Coat Roughness": 0.08})
    nt = tm.node_tree
    tc = nt.nodes.new("ShaderNodeTexCoord")
    sep = nt.nodes.new("ShaderNodeSeparateXYZ")
    ramp = nt.nodes.new("ShaderNodeValToRGB")
    nt.links.new(tc.outputs["Object"], sep.inputs[0])
    mapr = nt.nodes.new("ShaderNodeMapRange")
    mapr.inputs["From Min"].default_value = -TILE / 2
    mapr.inputs["From Max"].default_value = TILE / 2
    nt.links.new(sep.outputs["Y"], mapr.inputs["Value"])
    nt.links.new(mapr.outputs["Result"], ramp.inputs["Fac"])
    ramp.color_ramp.elements[0].color = srgb("#07080b")
    ramp.color_ramp.elements[1].color = srgb("#22252e")
    nt.links.new(ramp.outputs["Color"], tp.inputs["Base Color"])
    tile.data.materials.append(tm)

    # ---- the glyph: three rounded bars, heights 7 / 12 / 9 like the menu bar ----
    BAR_W = 1.4
    GAP = 0.62
    UNIT = 0.4
    heights = [7, 12, 9]
    base_y = -2.4
    total_w = 3 * BAR_W + 2 * GAP
    colors = [("#2dd4bf", "#a3e635"), ("#22c55e", "#facc15"), ("#14b8a6", "#84cc16")]
    for i, hgt in enumerate(heights):
        h = hgt * UNIT
        x = -total_w / 2 + BAR_W / 2 + i * (BAR_W + GAP)
        bar = rounded_box(f"bar{i}", BAR_W, h, 0.5, BAR_W / 2 - 0.001, 32)
        bar.location = (x, base_y + h / 2, 0.25)
        m, p = principled(f"bar{i}", **{"Roughness": 0.18, "Coat Weight": 1.0, "Coat Roughness": 0.03,
                                         "Emission Strength": 0.55})
        t = m.node_tree
        tco = t.nodes.new("ShaderNodeTexCoord")
        sp = t.nodes.new("ShaderNodeSeparateXYZ")
        mr = t.nodes.new("ShaderNodeMapRange")
        mr.inputs["From Min"].default_value = -h / 2
        mr.inputs["From Max"].default_value = h / 2
        rp = t.nodes.new("ShaderNodeValToRGB")
        rp.color_ramp.elements[0].color = srgb(colors[i][0])
        rp.color_ramp.elements[1].color = srgb(colors[i][1])
        t.links.new(tco.outputs["Object"], sp.inputs[0])
        t.links.new(sp.outputs["Y"], mr.inputs["Value"])
        t.links.new(mr.outputs["Result"], rp.inputs["Fac"])
        t.links.new(rp.outputs["Color"], p.inputs["Base Color"])
        t.links.new(rp.outputs["Color"], p.inputs["Emission Color"])
        bar.data.materials.append(m)
        glows.append(p)
        bars.append(bar)

    objs = [tile] + bars
    return objs, glows, None


if __name__ == "__main__":
    argv = sys.argv[sys.argv.index("--") + 1:] if "--" in sys.argv else []
    OUT = argv[0] if argv else "/tmp/hallmonitor-icon.png"
    SIZE = int(argv[1]) if len(argv) > 1 else 1024
    bpy.ops.wm.read_factory_settings(use_empty=True)
    scene = bpy.context.scene
    scene.render.engine = "BLENDER_EEVEE"
    scene.render.resolution_x = scene.render.resolution_y = SIZE
    scene.render.film_transparent = True
    scene.render.image_settings.file_format = "PNG"
    scene.render.image_settings.color_mode = "RGBA"
    scene.view_settings.view_transform = "AgX"
    scene.view_settings.look = "AgX - Medium High Contrast"
    scene.eevee.taa_render_samples = 128
    scene.eevee.use_shadows = True
    scene.eevee.use_raytracing = True

    world = bpy.data.worlds.new("w")
    scene.world = world
    world.use_nodes = True
    bg = world.node_tree.nodes["Background"]
    bg.inputs[0].default_value = (0.02, 0.022, 0.03, 1)
    bg.inputs[1].default_value = 0.6



    objs, _, _ = build()
    # ---- light: a soft key from the top, a rim from behind ----
    def area(name, loc, rot, energy, size, color=(1, 1, 1)):
        d = bpy.data.lights.new(name, "AREA")
        d.energy = energy
        d.size = size
        d.color = color
        o = bpy.data.objects.new(name, d)
        o.location = loc
        o.rotation_euler = rot
        scene.collection.objects.link(o)


    area("key", (0, 7, 10), (math.radians(-35), 0, 0), 500, 14)
    area("fill", (-7, -4, 6), (math.radians(30), math.radians(-45), 0), 160, 6, (0.75, 0.85, 1))
    area("rim", (5, 2, 3), (math.radians(-10), math.radians(60), 0), 220, 3, (1, 0.9, 0.75))

    # ---- camera: straight on, so the tile is exactly 824 of 1024 px ----
    cam_d = bpy.data.cameras.new("cam")
    cam_d.type = "ORTHO"
    cam_d.ortho_scale = TILE * 1024 / 824
    cam = bpy.data.objects.new("cam", cam_d)
    cam.location = (0, 0, 20)
    scene.collection.objects.link(cam)
    scene.camera = cam

    # A little bloom on the glowing bars.
    try:
        ng = bpy.data.node_groups.new("Comp", "CompositorNodeTree")
        ng.interface.new_socket("Image", in_out="OUTPUT", socket_type="NodeSocketColor")
        rl = ng.nodes.new("CompositorNodeRLayers")
        gl = ng.nodes.new("CompositorNodeGlare")
        out = ng.nodes.new("NodeGroupOutput")
        gl.inputs["Type"].default_value = "Bloom"
        gl.inputs["Threshold"].default_value = 1.0
        gl.inputs["Strength"].default_value = 0.35
        ng.links.new(rl.outputs["Image"], gl.inputs["Image"])
        ng.links.new(gl.outputs["Image"], out.inputs[0])
        scene.compositing_node_group = ng
    except Exception as e:
        print("compositor:", e)

    scene.render.filepath = OUT
    bpy.ops.render.render(write_still=True)
    print("wrote", OUT)
